package logs

import (
	"context"
	"io"
	"sort"
	"sync"
	"time"
)

// Edge kinds. Log availability has edges, and naming them is what keeps a
// crash loop from reading as a quiet period.
const (
	// EdgeHorizon marks where a stream begins. Earlier output existed; the
	// kubelet no longer has it.
	EdgeHorizon = "horizon"
	// EdgeRestart marks previous-instance output, which reaches exactly one
	// restart back.
	EdgeRestart = "restart"
	// EdgeGone marks a pod that left the workload. Its logs went with it.
	EdgeGone = "gone"
	// EdgeError marks a stream that could not be read, with the reason.
	EdgeError = "error"
	// EdgeEnded marks a stream that finished, which is a container that
	// stopped rather than a gap in what was retained.
	EdgeEnded = "ended"
)

// Edge is one boundary in what is knowable.
type Edge struct {
	Kind      string
	Pod       string
	Container string
	Time      time.Time
	Reason    string
}

// Target is one container instance to read.
type Target struct {
	Namespace string
	Pod       string
	Container string
	// Init marks an init container, which is behind an explicit toggle: its
	// output is usually setup noise, until the setup is what broke.
	Init bool
	// Previous asks for the instance before the last restart.
	Previous bool
}

func (t Target) key() string { return t.Pod + "/" + t.Container }

// OpenOptions is what a stream is opened with.
type OpenOptions struct {
	Follow    bool
	Previous  bool
	TailLines int64
	SinceTime *time.Time
}

// Source is the cluster side of a log stream, kept an interface so the merge
// logic is tested without an apiserver.
type Source interface {
	// Targets lists the container instances currently backing the workload.
	// It is called again on every refresh, which is how a rollout's new
	// replicas join the stream.
	Targets(ctx context.Context) ([]Target, error)
	Open(ctx context.Context, t Target, opts OpenOptions) (io.ReadCloser, error)
}

// Options configures a Streamer.
type Options struct {
	// Buffer is the ring size. Zero takes DefaultBufferLines.
	Buffer int
	// Containers, when set, restricts to these container names.
	Containers []string
	// Pods, when set, restricts to these pods. Per-pod is a filter on the
	// merged stream, not a different way in.
	Pods []string
	// IncludeSidecars reveals mesh proxies, hidden by default so their access
	// lines do not bury application output.
	IncludeSidecars bool
	// IncludeInit reveals init containers.
	IncludeInit bool
	// Previous reads the instance before the last restart.
	Previous bool
	// Tail is how many lines to ask for on open. Zero asks for the source's
	// default.
	Tail int64
	// Since bounds the window, for binding logs to a timeline selection.
	Since *time.Time
	// Refresh is how often the target list is re-read.
	Refresh time.Duration

	OnLine func(Line)
	OnEdge func(Edge)
}

// DefaultRefresh is how often the pod set is re-read, so a rollout's replicas
// join within a few seconds without watching every pod in the cluster.
const DefaultRefresh = 3 * time.Second

// Streamer merges every replica of one workload into one buffer.
type Streamer struct {
	src  Source
	opts Options
	buf  *Buffer

	mu     sync.Mutex
	active map[string]context.CancelFunc
	// known is every target seen in the current membership, whether or not a
	// stream is still open for it. A container that finished still belongs to
	// the workload; only leaving the pod set means its logs are gone.
	known map[string]bool
	// done marks streams that ended on their own. A finished container has
	// nothing more to say, and reopening it would replay its output forever.
	done  map[string]bool
	edges []Edge
	// horizonSeen keeps the availability marker to one per container, rather
	// than one per reconnect.
	horizonSeen map[string]bool
}

// NewStreamer builds a streamer. Nothing is read until Run.
func NewStreamer(src Source, opts Options) *Streamer {
	if opts.Refresh <= 0 {
		opts.Refresh = DefaultRefresh
	}
	return &Streamer{
		src:         src,
		opts:        opts,
		buf:         NewBuffer(opts.Buffer),
		active:      map[string]context.CancelFunc{},
		known:       map[string]bool{},
		done:        map[string]bool{},
		horizonSeen: map[string]bool{},
	}
}

// Buffer is the merged ring.
func (s *Streamer) Buffer() *Buffer { return s.buf }

// Edges are the availability boundaries recorded so far.
func (s *Streamer) Edges() []Edge {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Edge, len(s.edges))
	copy(out, s.edges)
	return out
}

// Run reads until ctx is cancelled.
func (s *Streamer) Run(ctx context.Context) {
	defer s.stopAll()
	for {
		s.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.opts.Refresh):
		}
	}
}

// reconcile opens streams for targets that gained one and closes those whose
// pod left the workload.
func (s *Streamer) reconcile(ctx context.Context) {
	targets, err := s.src.Targets(ctx)
	if err != nil {
		s.edge(Edge{Kind: EdgeError, Reason: err.Error(), Time: time.Now()})
		return
	}

	wanted := map[string]Target{}
	for _, t := range targets {
		if !s.want(t) {
			continue
		}
		wanted[t.key()] = t
	}

	s.mu.Lock()
	var gone []string
	for k := range s.known {
		if _, ok := wanted[k]; !ok {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	for k := range wanted {
		s.known[k] = true
	}
	var start []Target
	for k, t := range wanted {
		if s.active[k] == nil && !s.done[k] {
			start = append(start, t)
		}
	}
	sort.Slice(start, func(i, j int) bool { return start[i].key() < start[j].key() })
	s.mu.Unlock()

	for _, k := range gone {
		s.stop(k)
		pod, container := splitKey(k)
		s.edge(Edge{
			Kind: EdgeGone, Pod: pod, Container: container, Time: time.Now(),
			Reason: "pod left the workload; the kubelet no longer serves its logs",
		})
	}
	for _, t := range start {
		s.open(ctx, t)
	}
}

// want applies the container and pod filters.
func (s *Streamer) want(t Target) bool {
	if t.Init && !s.opts.IncludeInit {
		return false
	}
	if !t.Init && HiddenByDefault(t.Container) && !s.opts.IncludeSidecars {
		return false
	}
	if len(s.opts.Containers) > 0 && !contains(s.opts.Containers, t.Container) {
		return false
	}
	if len(s.opts.Pods) > 0 && !contains(s.opts.Pods, t.Pod) {
		return false
	}
	return true
}

func (s *Streamer) open(ctx context.Context, t Target) {
	sctx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	if s.active[t.key()] != nil || s.done[t.key()] {
		s.mu.Unlock()
		cancel()
		return
	}
	s.active[t.key()] = cancel
	s.mu.Unlock()

	go s.read(sctx, t)
}

func (s *Streamer) read(ctx context.Context, t Target) {
	defer s.release(t.key())

	rc, err := s.src.Open(ctx, t, OpenOptions{
		Follow:    true,
		Previous:  t.Previous || s.opts.Previous,
		TailLines: s.opts.Tail,
		SinceTime: s.opts.Since,
	})
	if err != nil {
		// One unreadable replica must not take the others with it. The reason
		// is recorded and the stream is retried on the next refresh, because a
		// container that is still starting becomes readable shortly.
		s.edge(Edge{Kind: EdgeError, Pod: t.Pod, Container: t.Container, Time: time.Now(), Reason: err.Error()})
		return
	}
	defer rc.Close()

	s.markHorizon(t)

	err = Scan(rc, Meta{Pod: t.Pod, Container: t.Container, Previous: t.Previous || s.opts.Previous}, func(l Line) {
		stored := s.buf.Add(l)
		if s.opts.OnLine != nil {
			s.opts.OnLine(stored)
		}
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.edge(Edge{Kind: EdgeError, Pod: t.Pod, Container: t.Container, Time: time.Now(), Reason: err.Error()})
		return
	}

	// A clean end means the container stopped. Marking it done keeps the
	// refresh loop from reopening a stream with nothing left to give.
	s.finish(t.key())
	s.edge(Edge{
		Kind: EdgeEnded, Pod: t.Pod, Container: t.Container, Time: time.Now(),
		Reason: "the container stopped writing; this is the end of its output, not a gap",
	})
}

// markHorizon records where a stream starts and, for previous-instance reads,
// how far back that reaches.
func (s *Streamer) markHorizon(t Target) {
	s.mu.Lock()
	seen := s.horizonSeen[t.key()]
	s.horizonSeen[t.key()] = true
	s.mu.Unlock()
	if seen {
		return
	}

	s.edge(Edge{
		Kind: EdgeHorizon, Pod: t.Pod, Container: t.Container, Time: time.Now(),
		Reason: "logs begin here: the kubelet serves only what container rotation retained",
	})
	if t.Previous || s.opts.Previous {
		s.edge(Edge{
			Kind: EdgeRestart, Pod: t.Pod, Container: t.Container, Time: time.Now(),
			Reason: "output from the instance before the last restart; the kubelet keeps one restart back, never two",
		})
	}
}

func (s *Streamer) edge(e Edge) {
	s.mu.Lock()
	s.edges = append(s.edges, e)
	s.mu.Unlock()
	if s.opts.OnEdge != nil {
		s.opts.OnEdge(e)
	}
}

func (s *Streamer) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.active[key]; cancel != nil {
		cancel()
	}
	delete(s.active, key)
}

func (s *Streamer) finish(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done[key] = true
}

func (s *Streamer) stop(key string) {
	s.mu.Lock()
	cancel := s.active[key]
	delete(s.active, key)
	delete(s.known, key)
	// A pod that left may come back under the same name after a rollout, so
	// its finished marker goes with it.
	delete(s.done, key)
	delete(s.horizonSeen, key)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Streamer) stopAll() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.active))
	for _, c := range s.active {
		cancels = append(cancels, c)
	}
	s.active = map[string]context.CancelFunc{}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

func splitKey(k string) (pod, container string) {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == '/' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

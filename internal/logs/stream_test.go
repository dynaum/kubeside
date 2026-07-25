package logs

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource is a workload whose pods and output a test controls.
type fakeSource struct {
	mu      sync.Mutex
	targets []Target
	streams map[string]string // key -> body
	fail    map[string]error
	opened  map[string]int
}

func newFake() *fakeSource {
	return &fakeSource{streams: map[string]string{}, fail: map[string]error{}, opened: map[string]int{}}
}

func key(t Target) string { return t.Pod + "/" + t.Container }

func (f *fakeSource) Targets(context.Context) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Target, len(f.targets))
	copy(out, f.targets)
	return out, nil
}

func (f *fakeSource) Open(_ context.Context, t Target, _ OpenOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened[key(t)]++
	if err := f.fail[key(t)]; err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(f.streams[key(t)])), nil
}

func (f *fakeSource) set(targets []Target, streams map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = targets
	for k, v := range streams {
		f.streams[k] = v
	}
}

func (f *fakeSource) openCount(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened[k]
}

// waitFor polls until cond holds or the deadline passes, so tests synchronize
// on the streamer's output rather than on a sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T, src Source, opts Options) *Streamer {
	t.Helper()
	if opts.Refresh == 0 {
		opts.Refresh = 10 * time.Millisecond
	}
	s := NewStreamer(src, opts)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	return s
}

// The whole point: six replicas, one merged stream, in real time order.
func TestStreamerMergesEveryReplicaInTimeOrder(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-2", Container: "app"},
	}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z one\n2026-07-24T10:00:02Z three\n",
		"checkout-2/app": "2026-07-24T10:00:01Z two\n2026-07-24T10:00:03Z four\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "four lines", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 4 })

	lines, _ := s.Buffer().Snapshot()
	if got := texts(lines); got != "one two three four" {
		t.Fatalf("merged order = %q", got)
	}
	if lines[0].Pod != "checkout-1" || lines[1].Pod != "checkout-2" {
		t.Errorf("lines lost their pod: %+v", lines[:2])
	}
}

func TestStreamerHidesMeshSidecarsUnlessAsked(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-1", Container: "istio-proxy"},
	}, map[string]string{
		"checkout-1/app":         "2026-07-24T10:00:00Z app line\n",
		"checkout-1/istio-proxy": "2026-07-24T10:00:01Z GET /healthz 200\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "the app line", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 1 })
	time.Sleep(30 * time.Millisecond)

	lines, _ := s.Buffer().Snapshot()
	if len(lines) != 1 || lines[0].Text != "app line" {
		t.Fatalf("lines = %v, want only application output", texts(lines))
	}
	if f.openCount("checkout-1/istio-proxy") != 0 {
		t.Error("a hidden sidecar stream was opened anyway")
	}
}

func TestStreamerIncludesSidecarsOnRequest(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-1", Container: "istio-proxy"},
	}, map[string]string{
		"checkout-1/app":         "2026-07-24T10:00:00Z app line\n",
		"checkout-1/istio-proxy": "2026-07-24T10:00:01Z GET /healthz 200\n",
	})

	s := run(t, f, Options{IncludeSidecars: true})
	waitFor(t, "both lines", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 2 })
}

func TestStreamerSkipsInitContainersUnlessAsked(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-1", Container: "migrate", Init: true},
	}, map[string]string{
		"checkout-1/app":     "2026-07-24T10:00:01Z app line\n",
		"checkout-1/migrate": "2026-07-24T10:00:00Z migrating\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "the app line", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 1 })
	time.Sleep(30 * time.Millisecond)
	if lines, _ := s.Buffer().Snapshot(); len(lines) != 1 {
		t.Fatalf("lines = %v, want the init container excluded", texts(lines))
	}

	s2 := run(t, f, Options{IncludeInit: true})
	waitFor(t, "both lines", func() bool { lines, _ := s2.Buffer().Snapshot(); return len(lines) == 2 })
}

// Per-pod is a filter on the merged stream, never a different entry point.
func TestStreamerPodFilter(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-2", Container: "app"},
	}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z from one\n",
		"checkout-2/app": "2026-07-24T10:00:01Z from two\n",
	})

	s := run(t, f, Options{Pods: []string{"checkout-2"}})
	waitFor(t, "one line", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 1 })
	time.Sleep(30 * time.Millisecond)

	lines, _ := s.Buffer().Snapshot()
	if len(lines) != 1 || lines[0].Pod != "checkout-2" {
		t.Fatalf("lines = %+v, want only checkout-2", lines)
	}
}

// A rollout replaces pods under the reader. The new replica must join the
// stream without the developer doing anything.
func TestStreamerPicksUpAPodThatAppearsLater(t *testing.T) {
	f := newFake()
	f.set([]Target{{Pod: "checkout-1", Container: "app"}}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z old replica\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "the first line", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 1 })

	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-2", Container: "app"},
	}, map[string]string{
		"checkout-2/app": "2026-07-24T10:00:01Z new replica\n",
	})

	waitFor(t, "the new replica", func() bool {
		lines, _ := s.Buffer().Snapshot()
		return len(lines) == 2 && lines[1].Pod == "checkout-2"
	})
}

// A deleted pod's logs are gone. Saying so is the difference between a crash
// loop and a quiet period.
func TestStreamerMarksAPodThatDisappears(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-2", Container: "app"},
	}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z one\n",
		"checkout-2/app": "2026-07-24T10:00:01Z two\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "both lines", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 2 })

	f.set([]Target{{Pod: "checkout-1", Container: "app"}}, nil)

	waitFor(t, "a gone edge", func() bool {
		for _, e := range s.Edges() {
			if e.Kind == EdgeGone && e.Pod == "checkout-2" {
				return true
			}
		}
		return false
	})
}

// One unreadable pod must not take the other five with it.
func TestStreamerRecordsAnOpenFailureAndKeepsTheRest(t *testing.T) {
	f := newFake()
	f.set([]Target{
		{Pod: "checkout-1", Container: "app"},
		{Pod: "checkout-2", Container: "app"},
	}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z healthy replica\n",
	})
	f.fail["checkout-2/app"] = errors.New("container is in ContainerCreating")

	s := run(t, f, Options{})
	waitFor(t, "an error edge", func() bool {
		for _, e := range s.Edges() {
			if e.Kind == EdgeError && e.Pod == "checkout-2" && strings.Contains(e.Reason, "ContainerCreating") {
				return true
			}
		}
		return false
	})

	lines, _ := s.Buffer().Snapshot()
	if len(lines) != 1 || lines[0].Pod != "checkout-1" {
		t.Fatalf("lines = %+v, want the readable replica's output", lines)
	}
}

// Where a stream begins is not where the workload began. The kubelet serves
// only what rotation retained, and the screen has to be able to say so.
func TestStreamerMarksTheAvailabilityHorizon(t *testing.T) {
	f := newFake()
	f.set([]Target{{Pod: "checkout-1", Container: "app"}}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z first retained line\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "a horizon edge", func() bool {
		for _, e := range s.Edges() {
			if e.Kind == EdgeHorizon && e.Pod == "checkout-1" {
				return true
			}
		}
		return false
	})
}

// Previous-container output reaches exactly one restart back. The edge says
// that, so nobody reads the gap as silence.
func TestStreamerPreviousContainerIsMarked(t *testing.T) {
	f := newFake()
	f.set([]Target{{Pod: "checkout-1", Container: "app", Previous: true}}, map[string]string{
		"checkout-1/app": "2026-07-24T09:59:59Z last words before the crash\n",
	})

	s := run(t, f, Options{Previous: true})
	waitFor(t, "the previous-instance line", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 1 })

	lines, _ := s.Buffer().Snapshot()
	if !lines[0].Previous {
		t.Error("a previous-instance line is not marked as one")
	}
	found := false
	for _, e := range s.Edges() {
		if e.Kind == EdgeRestart && strings.Contains(e.Reason, "one restart back") {
			found = true
		}
	}
	if !found {
		t.Error("no edge explains that previous logs reach exactly one restart back")
	}
}

// A container that finished has nothing more to say. Reopening it every
// refresh would hammer the apiserver and replay the same lines forever.
func TestStreamerDoesNotReopenAFinishedStream(t *testing.T) {
	f := newFake()
	f.set([]Target{{Pod: "checkout-1", Container: "app"}}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z done\n",
	})

	s := run(t, f, Options{})
	waitFor(t, "the line", func() bool { lines, _ := s.Buffer().Snapshot(); return len(lines) == 1 })
	time.Sleep(60 * time.Millisecond)

	if n := f.openCount("checkout-1/app"); n != 1 {
		t.Fatalf("opened %d times; a finished stream must not be reopened", n)
	}
	if lines, _ := s.Buffer().Snapshot(); len(lines) != 1 {
		t.Fatalf("lines = %v; the same output was replayed", texts(lines))
	}
}

// Every new line notifies, so the transport pushes rather than polls.
func TestStreamerNotifiesOnEachLine(t *testing.T) {
	f := newFake()
	f.set([]Target{{Pod: "checkout-1", Container: "app"}}, map[string]string{
		"checkout-1/app": "2026-07-24T10:00:00Z one\n2026-07-24T10:00:01Z two\n",
	})

	var mu sync.Mutex
	var seen []string
	s := run(t, f, Options{OnLine: func(l Line) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, l.Text)
	}})
	_ = s

	waitFor(t, "both notifications", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	})
}

// Package logs merges the output of every replica of one workload.
//
// The unit is the workload, not the pod. A developer debugging a deployment
// does not care which of six replicas served the failing request, and making
// them open one tab per replica is the complaint this screen exists to answer.
// Per-pod is a filter applied to the merged stream, never the way in.
//
// Everything here stays in memory. The ring buffer is capped per workload and
// counts what it dropped, because a buffer that quietly loses lines makes a
// chatty workload look calm.
package logs

import (
	"bufio"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBufferLines is the ring buffer size per workload, from
	// docs/05-architecture.md.
	DefaultBufferLines = 10_000

	// MaxLineBytes caps one line. Applications emit megabyte JSON blobs; the
	// stream must survive them, so the line is truncated and marked rather
	// than dropped or allowed to consume the buffer.
	MaxLineBytes = 64 * 1024

	// ReorderWindow is how far back a newly arrived line may be inserted once a
	// stream is live. Replicas do not arrive in lockstep, and a merged view
	// that ignores that reads out of order. Beyond the window the reader has
	// already moved on, so a line lands at the end and says it arrived late.
	ReorderWindow = 5 * time.Second

	// SettleWindow is how long after a subscription every line merges strictly
	// by timestamp, however old.
	//
	// Opening a workload replays each replica's retained scrollback at once,
	// and that backlog can span hours. Judging those lines by the live reorder
	// window would order the first screen by which kubelet answered first,
	// which is exactly the wrong answer for the screen a developer is looking
	// at when they arrive.
	SettleWindow = 3 * time.Second
)

// Meta is what every line from one stream carries.
type Meta struct {
	Pod       string
	Container string
	// Previous marks output from the instance before the last restart. The
	// kubelet keeps exactly one back, never two.
	Previous bool
}

// Line is one log line, merged.
type Line struct {
	Time      time.Time
	Pod       string
	Container string
	Text      string
	Previous  bool
	// Truncated marks a line cut at MaxLineBytes.
	Truncated bool
	// Late marks a line that arrived too long after its timestamp to be
	// placed in order. Saying so beats silently misplacing it.
	Late bool
	// Seq is the buffer's monotonic sequence, so a client can ask for
	// everything after what it already has.
	Seq int64
}

// ParseLine splits the RFC3339Nano stamp the kubelet prefixes when logs are
// requested with timestamps. A line without one keeps its whole text: guessing
// a time would place it wrongly in the merge, and the caller can decide.
func ParseLine(raw string) (time.Time, string, bool) {
	stamp, rest, found := strings.Cut(raw, " ")
	if !found {
		// A stamp with no text after it is still a stamped, empty line.
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t, "", true
		}
		return time.Time{}, raw, false
	}
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}, raw, false
	}
	return t, rest, true
}

// Scan reads a log stream and emits one Line per record.
//
// It reads incrementally rather than buffering the stream, so follow mode
// delivers a line the moment the kubelet does.
func Scan(r io.Reader, meta Meta, emit func(Line)) error {
	br := bufio.NewReaderSize(r, 8*1024)
	for {
		raw, truncated, err := readLine(br)
		if raw != "" || err == nil {
			when, text, _ := ParseLine(raw)
			emit(Line{
				Time:      when,
				Pod:       meta.Pod,
				Container: meta.Container,
				Previous:  meta.Previous,
				Text:      text,
				Truncated: truncated,
			})
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// readLine returns one line, capped at MaxLineBytes. The remainder of an
// oversized line is discarded so the next line still parses.
func readLine(br *bufio.Reader) (line string, truncated bool, err error) {
	var b strings.Builder
	for {
		chunk, more, e := br.ReadLine()
		if len(chunk) > 0 {
			if b.Len() < MaxLineBytes {
				room := MaxLineBytes - b.Len()
				if len(chunk) > room {
					chunk, truncated = chunk[:room], true
				}
				b.Write(chunk)
			} else {
				truncated = true
			}
		}
		if e != nil {
			return b.String(), truncated, e
		}
		if !more {
			return b.String(), truncated, nil
		}
	}
}

// Buffer is the per-workload ring.
type Buffer struct {
	mu      sync.Mutex
	cap     int
	lines   []Line
	dropped int
	seq     int64
	now     func() time.Time
	// settleUntil is when the initial backlog stops merging unconditionally by
	// timestamp and the live reorder window takes over.
	settleUntil time.Time
	// lastSeen is the timestamp carried forward onto unstamped lines, so a
	// stack trace stays attached to the panic that produced it.
	lastSeen time.Time
}

// NewBuffer makes a ring holding at most n lines.
func NewBuffer(n int) *Buffer {
	if n <= 0 {
		n = DefaultBufferLines
	}
	now := time.Now
	return &Buffer{cap: n, lines: make([]Line, 0, n), now: now, settleUntil: now().Add(SettleWindow)}
}

// endSettle ends the backlog phase immediately. Tests use it to exercise live
// ordering without waiting.
func (b *Buffer) endSettle() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settleUntil = time.Time{}
}

// Add places a line in time order and returns it as stored.
//
// A line stamped inside the reorder window is inserted where it belongs. One
// stamped further back is appended and marked late: the reader has already
// scrolled past that moment, and quietly rewriting history behind them is
// worse than admitting the line arrived out of order.
func (b *Buffer) Add(l Line) Line {
	b.mu.Lock()
	defer b.mu.Unlock()

	if l.Time.IsZero() {
		// Unstamped output belongs with the line it followed, not at the
		// epoch.
		l.Time = b.lastSeen
	} else if l.Time.After(b.lastSeen) {
		b.lastSeen = l.Time
	}

	b.seq++
	l.Seq = b.seq

	settling := b.now().Before(b.settleUntil)

	at := len(b.lines)
	if n := len(b.lines); n > 0 && l.Time.Before(b.lines[n-1].Time) {
		if !settling && b.lines[n-1].Time.Sub(l.Time) > ReorderWindow {
			l.Late = true
		} else {
			at = sort.Search(n, func(i int) bool { return b.lines[i].Time.After(l.Time) })
		}
	}

	if at == len(b.lines) {
		b.lines = append(b.lines, l)
	} else {
		b.lines = append(b.lines, Line{})
		copy(b.lines[at+1:], b.lines[at:])
		b.lines[at] = l
	}

	if len(b.lines) > b.cap {
		over := len(b.lines) - b.cap
		b.lines = append(b.lines[:0], b.lines[over:]...)
		b.dropped += over
	}
	return l
}

// Snapshot returns everything held, oldest first, and how many lines the ring
// has dropped since it started.
func (b *Buffer) Snapshot() ([]Line, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Line, len(b.lines))
	copy(out, b.lines)
	return out, b.dropped
}

// Since returns the lines added after seq, which is how a follower catches up
// without re-reading the window it already has.
func (b *Buffer) Since(seq int64) ([]Line, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Line, 0, 16)
	for _, l := range b.lines {
		if l.Seq > seq {
			out = append(out, l)
		}
	}
	return out, b.dropped
}

// Seq is the sequence of the most recently added line.
func (b *Buffer) Seq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// hidden lists the containers whose output is suppressed unless asked for.
// Mesh proxies emit an access line per request, which buries the application
// output this screen exists to show.
var hidden = map[string]bool{
	"istio-proxy":   true,
	"istio-init":    true,
	"linkerd-proxy": true,
	"linkerd-init":  true,
	"envoy":         true,
}

// HiddenByDefault reports whether a container is infrastructure rather than
// application output. It matches exact names only: "envoy-config-reloader" is
// somebody's application and stays visible.
func HiddenByDefault(container string) bool { return hidden[container] }

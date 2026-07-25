package api

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dynaum/kubeside/internal/logs"
)

// fakeLogSource is one workload's output, controlled by the test.
type fakeLogSource struct {
	mu      sync.Mutex
	targets []logs.Target
	bodies  map[string]string
	opens   int
	closed  int
}

func (f *fakeLogSource) Targets(context.Context) ([]logs.Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]logs.Target, len(f.targets))
	copy(out, f.targets)
	return out, nil
}

func (f *fakeLogSource) Open(_ context.Context, t logs.Target, _ logs.OpenOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	body := f.bodies[t.Pod+"/"+t.Container]
	return &countingCloser{Reader: strings.NewReader(body), src: f}, nil
}

func (f *fakeLogSource) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

type countingCloser struct {
	io.Reader
	src *fakeLogSource
}

func (c *countingCloser) Close() error {
	c.src.mu.Lock()
	defer c.src.mu.Unlock()
	c.src.closed++
	return nil
}

func logStub(src logs.Source, err error) *streamStub {
	return &streamStub{view: view(app("team-a", "checkout", "healthy")), logSource: src, logErr: err}
}

func subscribeLogs(t *testing.T, c *websocket.Conn, ns, workload string) {
	t.Helper()
	send(t, c, ClientMessage{
		Type: msgSubscribe, View: ViewLogs, Context: "qa",
		Namespace: ns, Workload: workload,
	})
}

func TestLogsSubscriptionDeliversMergedLines(t *testing.T) {
	src := &fakeLogSource{
		targets: []logs.Target{
			{Pod: "checkout-1", Container: "app"},
			{Pod: "checkout-2", Container: "app"},
		},
		bodies: map[string]string{
			"checkout-1/app": "2026-07-24T10:00:00Z one\n2026-07-24T10:00:02Z three\n",
			"checkout-2/app": "2026-07-24T10:00:01Z two\n",
		},
	}
	s, hs := startStream(t, logStub(src, nil))
	c := mustDial(t, s, hs)
	subscribeLogs(t, c, "team-a", "checkout")

	got := collectLines(t, c, 3)
	if got != "one two three" {
		t.Fatalf("lines = %q, want every replica merged in time order", got)
	}
}

func TestLogsSubscriptionReportsAnUnavailableWorkload(t *testing.T) {
	s, hs := startStream(t, logStub(nil, errors.New(`no app "ghost" in team-a`)))
	c := mustDial(t, s, hs)
	subscribeLogs(t, c, "team-a", "ghost")

	m := read(t, c)
	if m.Type != msgError || !strings.Contains(m.Message, "ghost") {
		t.Fatalf("message = %+v, want an error naming the workload", m)
	}
}

func TestLogsSubscriptionRequiresAWorkload(t *testing.T) {
	s, hs := startStream(t, logStub(&fakeLogSource{}, nil))
	c := mustDial(t, s, hs)
	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewLogs, Context: "qa", Namespace: "team-a"})

	m := read(t, c)
	if m.Type != msgError {
		t.Fatalf("message = %+v, want an error for a subscription with no workload", m)
	}
}

// Availability edges reach the client, so the screen can mark where knowledge
// ends instead of rendering silence.
func TestLogsEdgesReachTheClient(t *testing.T) {
	src := &fakeLogSource{
		targets: []logs.Target{{Pod: "checkout-1", Container: "app"}},
		bodies:  map[string]string{"checkout-1/app": "2026-07-24T10:00:00Z hello\n"},
	}
	s, hs := startStream(t, logStub(src, nil))
	c := mustDial(t, s, hs)
	subscribeLogs(t, c, "team-a", "checkout")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := read(t, c)
		if m.Type != msgLogs || m.Logs == nil {
			continue
		}
		for _, e := range m.Logs.Edges {
			if e.Kind == logs.EdgeHorizon && strings.Contains(e.Reason, "rotation") {
				return
			}
		}
	}
	t.Fatal("no horizon edge arrived")
}

// A workload nobody is watching must not be streamed. Log streams are the most
// expensive thing this tool opens.
func TestLogsStopStreamingWhenTheLastSubscriberLeaves(t *testing.T) {
	src := &fakeLogSource{
		targets: []logs.Target{{Pod: "checkout-1", Container: "app"}},
		bodies:  map[string]string{"checkout-1/app": "2026-07-24T10:00:00Z hello\n"},
	}
	s, hs := startStream(t, logStub(src, nil))
	c := mustDial(t, s, hs)
	subscribeLogs(t, c, "team-a", "checkout")
	collectLines(t, c, 1)

	send(t, c, ClientMessage{Type: msgUnsubscribe, View: ViewLogs, Context: "qa", Namespace: "team-a", Workload: "checkout"})

	time.Sleep(80 * time.Millisecond)
	before := src.openCount()
	time.Sleep(150 * time.Millisecond)
	if after := src.openCount(); after != before {
		t.Fatalf("streams kept opening after unsubscribe: %d -> %d", before, after)
	}
}

// Two windows on the same workload share one set of streams. Doubling tabs
// must not double the load on the kubelet.
func TestLogsShareOneStreamerBetweenSubscribers(t *testing.T) {
	src := &fakeLogSource{
		targets: []logs.Target{{Pod: "checkout-1", Container: "app"}},
		bodies:  map[string]string{"checkout-1/app": "2026-07-24T10:00:00Z hello\n"},
	}
	s, hs := startStream(t, logStub(src, nil))

	a := mustDial(t, s, hs)
	b := mustDial(t, s, hs)
	subscribeLogs(t, a, "team-a", "checkout")
	collectLines(t, a, 1)
	subscribeLogs(t, b, "team-a", "checkout")

	// The second window gets the buffered output without reopening anything.
	if got := collectLines(t, b, 1); got != "hello" {
		t.Fatalf("second window saw %q, want the buffered line", got)
	}
	if n := src.openCount(); n != 1 {
		t.Fatalf("opened %d streams for one workload; subscribers are not sharing", n)
	}
}

// Different filters are different subscriptions: one window showing sidecars
// must not change what another sees.
func TestLogsFiltersAreSeparateSubscriptions(t *testing.T) {
	src := &fakeLogSource{
		targets: []logs.Target{
			{Pod: "checkout-1", Container: "app"},
			{Pod: "checkout-1", Container: "istio-proxy"},
		},
		bodies: map[string]string{
			"checkout-1/app":         "2026-07-24T10:00:00Z app line\n",
			"checkout-1/istio-proxy": "2026-07-24T10:00:01Z proxy line\n",
		},
	}
	s, hs := startStream(t, logStub(src, nil))
	c := mustDial(t, s, hs)

	subscribeLogs(t, c, "team-a", "checkout")
	if got := collectLines(t, c, 1); got != "app line" {
		t.Fatalf("default view = %q, want the sidecar hidden", got)
	}

	send(t, c, ClientMessage{
		Type: msgSubscribe, View: ViewLogs, Context: "qa",
		Namespace: "team-a", Workload: "checkout", IncludeSidecars: true,
	})
	if got := collectLinesMatching(t, c, "proxy line"); !got {
		t.Fatal("the sidecar view never delivered the proxy output")
	}
}

// collectLines reads log batches until n lines have arrived, returning their
// text in order. Duplicate sequence numbers are ignored: a subscriber joining
// mid-flush may see one batch twice, which the client dedupes by Seq.
func collectLines(t *testing.T, c *websocket.Conn, n int) string {
	t.Helper()
	seen := map[int64]bool{}
	var out []string
	deadline := time.Now().Add(5 * time.Second)
	for len(out) < n && time.Now().Before(deadline) {
		m := read(t, c)
		if m.Type != msgLogs || m.Logs == nil {
			continue
		}
		for _, l := range m.Logs.Lines {
			if seen[l.Seq] {
				continue
			}
			seen[l.Seq] = true
			out = append(out, l.Text)
		}
	}
	if len(out) < n {
		t.Fatalf("got %d lines, want %d: %v", len(out), n, out)
	}
	return strings.Join(out, " ")
}

// collectLinesMatching reports whether a line with the given text arrives.
func collectLinesMatching(t *testing.T, c *websocket.Conn, want string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := read(t, c)
		if m.Type != msgLogs || m.Logs == nil {
			continue
		}
		for _, l := range m.Logs.Lines {
			if l.Text == want {
				return true
			}
		}
	}
	return false
}

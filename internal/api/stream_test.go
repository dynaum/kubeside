package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dynaum/kubeside/internal/forward"
	"github.com/dynaum/kubeside/internal/guard"
	"github.com/dynaum/kubeside/internal/logs"
	"github.com/dynaum/kubeside/internal/promotion"
	"github.com/dynaum/kubeside/internal/rbac"
	"github.com/dynaum/kubeside/internal/resolved"
)

// streamStub is a mutable API whose app list a test can change between polls,
// standing in for a cluster where something happened.
type streamStub struct {
	mu        sync.Mutex
	view      AppsView
	err       error
	calls     int
	logSource logs.Source
	logErr    error
	changes   []string
}

func (s *streamStub) LogSource(_, _, _ string) (logs.Source, error) {
	if s.logErr != nil {
		return nil, s.logErr
	}
	return s.logSource, nil
}

// observed records what the feed reported, so a test can assert that a live
// change reached the session buffer.
func (s *streamStub) Observed(contextName string, before, after AppView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = append(s.changes, before.Health+"→"+after.Health)
}

func (s *streamStub) observedChanges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.changes))
	copy(out, s.changes)
	return out
}

func (s *streamStub) Gate(req GateRequest) (GateView, error) {
	if req.Context == "prod" {
		return GateView{
			Gate: guard.Gate{
				Environment: "prod", Risk: "high", Policy: "deny",
				Reason: "writes are denied in prod by the write policy in your kubeside config",
			},
			Permission: rbac.Permission{Allowed: true},
		}, nil
	}
	return GateView{
		Gate: guard.Gate{
			Permitted: true, Require: guard.RequireTypedName, Confirm: req.Name,
			Environment: "qa", Risk: "low", Policy: "confirm",
			Kubectl: "kubectl " + req.Verb + " " + req.Resource + " " + req.Name,
			Blast:   guard.Blast{Pods: 1, Summary: "one of four replicas"},
		},
		Permission: rbac.Permission{Allowed: true},
	}, nil
}

func (s *streamStub) Capabilities(contextName, namespace string) CapabilitiesView {
	allowed := contextName == "qa"
	can := map[string]rbac.Permission{}
	for _, a := range []rbac.Action{{Verb: "create", Resource: "pods", Subresource: "exec", Namespace: namespace}} {
		if allowed {
			can[a.Key()] = rbac.Permission{Allowed: true}
			continue
		}
		can[a.Key()] = rbac.Permission{Reason: "needs " + a.Key()}
	}
	return CapabilitiesView{Context: contextName, Namespace: namespace, Can: can}
}

func (s *streamStub) Promotion() PromotionView {
	envs := []promotion.Env{{Name: "qa"}, {Name: "prod"}}
	rows := promotion.Build(envs, []promotion.Instance{
		{Env: "qa", App: "checkout", Namespace: "team-a", Present: true, Tag: "v2"},
		{Env: "prod", App: "checkout", Namespace: "team-a", Present: true, Tag: "v1"},
	})
	return PromotionView{Envs: envs, Rows: rows, Summary: promotion.Summarize(rows)}
}

func (s *streamStub) StartForward(req ForwardRequest) (forward.Forward, error) {
	if req.RemotePort == 0 {
		return forward.Forward{}, errors.New("a container port is required")
	}
	return forward.Forward{
		ID: "fwd-1", Context: req.Context, Namespace: req.Namespace, Workload: req.Workload,
		RemotePort: req.RemotePort, LocalPort: 51234, Address: "127.0.0.1:51234",
		State: forward.StateReady, Environment: "qa",
	}, nil
}

func (s *streamStub) Forwards() []forward.Forward {
	return []forward.Forward{{ID: "fwd-1", Workload: "checkout", LocalPort: 51234, State: forward.StateReady}}
}

func (s *streamStub) StopForward(id string) error {
	if id != "fwd-1" {
		return errors.New("no forward " + id)
	}
	return nil
}

func (s *streamStub) Diff(req DiffRequest) (DiffView, error) {
	if req.Other == "unreachable" {
		return DiffView{}, errors.New("unreachable is not connected")
	}
	return DiffView{
		Left:      DiffSide{Context: req.Context, Namespace: req.Namespace, Workload: req.Workload},
		Right:     DiffSide{Context: req.Other, Namespace: req.Namespace, Workload: req.Workload},
		Container: "app",
		Rows: []resolved.CrossRow{
			{Key: "LOG_LEVEL", Left: "debug", Right: "debug", Class: resolved.ClassSuspicious},
			{Key: "RETRY_LIMIT", Left: "3", RightUnset: true, Class: resolved.ClassMissing},
		},
		Summary: resolved.Summary{Suspicious: 1, Missing: 1},
	}, nil
}

func (s *streamStub) RevealSecret(_, _, _, _, _ string) (RevealView, error) {
	return RevealView{}, errors.New("no reveal in this test")
}

func (s *streamStub) Config(_, _, _ string) (ConfigView, error) {
	return ConfigView{}, errors.New("no config in this test")
}

func (s *streamStub) AppDetail(_, _, _ string) (AppDetailView, error) {
	return AppDetailView{}, errors.New("no detail in this test")
}

func (s *streamStub) Timeline(_, _, _ string) (TimelineView, error) {
	return TimelineView{}, errors.New("no timeline in this test")
}

func (s *streamStub) Contexts() []ContextView {
	return []ContextView{{Name: "qa", Current: true, State: "live", HasData: true}}
}

func (s *streamStub) Apps(name string) (AppsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return AppsView{}, s.err
	}
	return s.view, nil
}

func (s *streamStub) set(v AppsView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.view = v
}

func (s *streamStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// startStream boots a real HTTP server over the real handler, so every test
// below goes through the same security middleware a browser would.
func startStream(t *testing.T, stub *streamStub) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(stub, nil, WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs
}

func dial(t *testing.T, s *Server, hs *httptest.Server, opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/api/stream?" + tokenParam + "=" + s.Token()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, url, opts)
}

func mustDial(t *testing.T, s *Server, hs *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := dial(t, s, hs, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, m ClientMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c, m); err != nil {
		t.Fatalf("write %s: %v", m.Type, err)
	}
}

func read(t *testing.T, c *websocket.Conn) ServerMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var m ServerMessage
	if err := wsjson.Read(ctx, c, &m); err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

// readUntil drains messages until one satisfies want, so a test asserting on a
// patch is not derailed by an intervening keepalive or a duplicate snapshot.
func readUntil(t *testing.T, c *websocket.Conn, want func(ServerMessage) bool) ServerMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := read(t, c)
		if want(m) {
			return m
		}
	}
	t.Fatal("no matching message before the deadline")
	return ServerMessage{}
}

// A websocket upgrade is not covered by the browser's same-origin policy, so
// the handshake must enforce the token exactly as the REST path does.
func TestStreamHandshakeRequiresTheToken(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+"/api/stream", nil)
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("a tokenless websocket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
	_ = s
}

func TestStreamHandshakeRejectsAForeignOrigin(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)

	c, resp, err := dial(t, s, hs, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("a cross-origin websocket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestStreamSendsASnapshotOnSubscribe(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})

	m := read(t, c)
	if m.Type != msgSnapshot || m.View != ViewApps || m.Context != "qa" {
		t.Fatalf("first message = %+v, want an apps snapshot for qa", m)
	}
	if m.Snapshot == nil || len(m.Snapshot.Apps) != 1 {
		t.Fatalf("snapshot = %+v, want the one app", m.Snapshot)
	}
}

func TestStreamPushesAPatchWhenAnAppChanges(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	if m := read(t, c); m.Type != msgSnapshot {
		t.Fatalf("first message = %+v, want a snapshot", m)
	}

	stub.set(view(app("team-a", "checkout", "failed"), app("team-b", "search", "healthy")))

	m := readUntil(t, c, func(m ServerMessage) bool { return m.Type == msgPatch })
	if m.Patch == nil {
		t.Fatal("patch message carried no patch")
	}
	if len(m.Patch.Changed) != 1 || m.Patch.Changed[0].Health != "failed" {
		t.Errorf("changed = %+v, want checkout failed", m.Patch.Changed)
	}
	if len(m.Patch.Added) != 1 || m.Patch.Added[0].Name != "search" {
		t.Errorf("added = %+v, want search", m.Patch.Added)
	}
}

// A cluster nobody is looking at must not be polled. This is the precursor to
// watch tiering: attention is what makes a connection active.
func TestStreamStopsPollingWhenTheLastSubscriberLeaves(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	read(t, c)
	send(t, c, ClientMessage{Type: msgUnsubscribe, View: ViewApps, Context: "qa"})

	// Let any in-flight poll finish, then take the reading the test compares
	// against.
	time.Sleep(80 * time.Millisecond)
	before := stub.callCount()
	time.Sleep(150 * time.Millisecond)
	if after := stub.callCount(); after != before {
		t.Fatalf("polls continued after unsubscribe: %d -> %d", before, after)
	}
}

func TestStreamStopsPollingWhenTheTabCloses(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	read(t, c)
	_ = c.Close(websocket.StatusNormalClosure, "done")

	time.Sleep(80 * time.Millisecond)
	before := stub.callCount()
	time.Sleep(150 * time.Millisecond)
	if after := stub.callCount(); after != before {
		t.Fatalf("polls continued after the tab closed: %d -> %d", before, after)
	}
}

// Nothing subscribed means nothing fetched: opening the UI must not wake every
// cluster in the kubeconfig.
func TestStreamPollsNothingUntilSubscribed(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	mustDial(t, s, hs)

	time.Sleep(100 * time.Millisecond)
	if got := stub.callCount(); got != 0 {
		t.Fatalf("polls = %d before any subscription, want 0", got)
	}
}

func TestStreamReportsAnUnknownContextAsAnError(t *testing.T) {
	stub := &streamStub{err: errors.New("unknown context \"nope\"")}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "nope"})

	m := read(t, c)
	if m.Type != msgError || !strings.Contains(m.Message, "nope") {
		t.Fatalf("message = %+v, want an error naming the context", m)
	}
}

func TestStreamRejectsAnUnknownView(t *testing.T) {
	stub := &streamStub{view: view()}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: "timeline", Context: "qa"})

	m := read(t, c)
	if m.Type != msgError {
		t.Fatalf("message = %+v, want an error for an unimplemented view", m)
	}
	if !strings.Contains(m.Message, "timeline") {
		t.Errorf("error %q should name the view it refused", m.Message)
	}
}

// Two tabs watching the same context share one poller. Doubling tabs must not
// double the load on the apiserver.
func TestStreamSharesOnePollerBetweenSubscribers(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)

	a := mustDial(t, s, hs)
	b := mustDial(t, s, hs)
	send(t, a, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	read(t, a)
	send(t, b, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	read(t, b)

	before := stub.callCount()
	time.Sleep(100 * time.Millisecond)
	polls := stub.callCount() - before
	// Ten intervals fit in the window; with two independent pollers it would
	// be about twice that. The bound is loose to stay green on a slow runner.
	if polls > 16 {
		t.Fatalf("%d polls in 100ms with a 10ms interval; subscribers are not sharing a poller", polls)
	}
}

// A tab that stops reading must not stall the poller or grow memory without
// bound. It is disconnected instead, and reconnects to a fresh snapshot.
func TestSlowSubscriberIsDroppedInsteadOfBlockingTheFeed(t *testing.T) {
	sub := newSubscriber(2)
	for i := 0; i < 50; i++ {
		sub.send(ServerMessage{Type: msgPatch, View: ViewApps, Context: "qa"})
	}
	if !sub.overflowed() {
		t.Fatal("a subscriber that never reads was not marked overflowed")
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("an overflowed subscriber must be closed so the client resyncs")
	}
}

// The websocket is a same-origin connection the CSP must actually permit.
func TestCSPAllowsTheLoopbackWebsocket(t *testing.T) {
	s := newTestServer(t)
	csp := do(t, s, "GET", "/", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "ws://127.0.0.1:*") || !strings.Contains(csp, "ws://localhost:*") {
		t.Errorf("CSP %q must permit the loopback websocket, or the live path silently dies in the browser", csp)
	}
	if strings.Contains(csp, "ws://*") {
		t.Errorf("CSP %q allows any websocket host", csp)
	}
}

// A rollout that fails while a developer is watching must land on the timeline,
// not only in the row colour. The feed already knows what moved.
func TestFeedReportsChangedRowsAsObservations(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	read(t, c)

	stub.set(view(app("team-a", "checkout", "failed")))
	readUntil(t, c, func(m ServerMessage) bool { return m.Type == msgPatch })

	got := stub.observedChanges()
	if len(got) == 0 || got[0] != "healthy→failed" {
		t.Fatalf("observations = %v, want the health transition", got)
	}
}

// An app that only gained a replica has not changed state. Recording it would
// bury the transitions that matter.
func TestFeedDoesNotReportRowsThatOnlyGrew(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c := mustDial(t, s, hs)

	send(t, c, ClientMessage{Type: msgSubscribe, View: ViewApps, Context: "qa"})
	read(t, c)

	grown := app("team-a", "checkout", "healthy")
	grown.Ready = "4/4"
	stub.set(view(grown))
	readUntil(t, c, func(m ServerMessage) bool { return m.Type == msgPatch })

	// The feed reports the row; the service decides it is not worth keeping.
	// Both halves are tested: here the row arrives with an unchanged health.
	for _, o := range stub.observedChanges() {
		if o != "healthy→healthy" {
			t.Fatalf("observation = %q, want the unchanged health passed through", o)
		}
	}
}

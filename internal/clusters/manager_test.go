package clusters

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/kubeconfig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeSession struct{ closed bool }

func (s *fakeSession) Close() error { s.closed = true; return nil }

// fakeConnector answers per context: an error to return, or a gate to block on.
type fakeConnector struct {
	mu       sync.Mutex
	errs     map[string]error
	gates    map[string]chan struct{}
	attempts map[string]int
}

func newConnector() *fakeConnector {
	return &fakeConnector{
		errs:     map[string]error{},
		gates:    map[string]chan struct{}{},
		attempts: map[string]int{},
	}
}

func (f *fakeConnector) Connect(ctx context.Context, kctx kubeconfig.Context) (Session, error) {
	f.mu.Lock()
	f.attempts[kctx.Name]++
	err := f.errs[kctx.Name]
	gate := f.gates[kctx.Name]
	f.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &fakeSession{}, nil
}

func (f *fakeConnector) attemptsFor(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[name]
}

func testConfig() *kubeconfig.Config {
	return &kubeconfig.Config{
		Current: "stg",
		Contexts: []kubeconfig.Context{
			{Name: "prod", Server: "https://prod:6443"},
			{Name: "qa", Server: "https://qa:6443"},
			{Name: "stg", Server: "https://stg:6443", IsCurrent: true},
		},
	}
}

func newManager(t *testing.T, fc *fakeConnector) (*Manager, *fakeClock) {
	t.Helper()
	clk := &fakeClock{now: time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)}
	m := New(testConfig(), fc, Options{Clock: clk, IdleAfter: 15 * time.Minute, BreakerBase: time.Minute, BreakerMax: 8 * time.Minute})
	t.Cleanup(m.Close)
	return m, clk
}

func mustStatus(t *testing.T, m *Manager, name string) Status {
	t.Helper()
	s, ok := m.Status(name)
	if !ok {
		t.Fatalf("no status for %q", name)
	}
	return s
}

func TestNothingConnectsUntilAsked(t *testing.T) {
	fc := newConnector()
	m, _ := newManager(t, fc)

	for _, n := range []string{"qa", "stg", "prod"} {
		if got := mustStatus(t, m, n).State; got != StateNeverConnected {
			t.Errorf("%s = %s, want never-connected before any use", n, got)
		}
		if fc.attemptsFor(n) != 0 {
			t.Errorf("%s was contacted at startup; connection must be lazy", n)
		}
	}
}

func TestCurrentContextConnectsFirst(t *testing.T) {
	fc := newConnector()
	m, _ := newManager(t, fc)

	got := m.ConnectOrder()
	if len(got) == 0 || got[0] != "stg" {
		t.Fatalf("order = %v, want the current context first so the developer's usual workspace renders first", got)
	}
}

func TestSuccessfulConnectGoesLive(t *testing.T) {
	fc := newConnector()
	m, _ := newManager(t, fc)

	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := mustStatus(t, m, "qa").State; got != StateLive {
		t.Fatalf("state = %s, want live", got)
	}
}

// The distinction this product cannot get wrong.
func TestStaleIsNotNeverConnected(t *testing.T) {
	fc := newConnector()
	m, clk := newManager(t, fc)

	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	clk.advance(20 * time.Minute)

	if reaped := m.ReapIdle(); len(reaped) != 1 || reaped[0] != "qa" {
		t.Fatalf("reaped = %v, want [qa]", reaped)
	}

	qa := mustStatus(t, m, "qa")
	if qa.State != StateStale {
		t.Fatalf("state = %s, want stale; an idle disconnect must retain the snapshot", qa.State)
	}
	if !qa.State.HasData() {
		t.Error("stale must still report having data, labelled with its age")
	}
	if qa.Age < 20*time.Minute {
		t.Errorf("age = %s, want at least 20m so the UI can label the snapshot", qa.Age)
	}

	prod := mustStatus(t, m, "prod")
	if prod.State != StateNeverConnected {
		t.Fatalf("prod = %s, want never-connected", prod.State)
	}
	if prod.State.HasData() {
		t.Error("never-connected must report no data; showing an empty panel as though it were current is the failure mode this guards")
	}
}

func TestTouchDefersIdleReap(t *testing.T) {
	fc := newConnector()
	m, clk := newManager(t, fc)
	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	clk.advance(14 * time.Minute)
	m.Touch("qa")
	clk.advance(10 * time.Minute)

	if reaped := m.ReapIdle(); len(reaped) != 0 {
		t.Fatalf("reaped %v, want none: the view was in use 10m ago", reaped)
	}
}

func TestIdleReapClosesTheSession(t *testing.T) {
	fc := newConnector()
	m, clk := newManager(t, fc)
	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	clk.advance(16 * time.Minute)
	m.ReapIdle()

	// Reconnect must be possible after a reap.
	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatalf("reconnect after reap: %v", err)
	}
	if got := mustStatus(t, m, "qa").State; got != StateLive {
		t.Fatalf("state = %s, want live after reconnect", got)
	}
}

func TestUnreachableAndUnauthorizedAreDistinct(t *testing.T) {
	fc := newConnector()
	fc.errs["prod"] = errors.New("dial tcp: i/o timeout")
	fc.errs["qa"] = apierrors.NewUnauthorized("token expired")
	m, _ := newManager(t, fc)

	_ = m.Connect(context.Background(), "prod")
	_ = m.Connect(context.Background(), "qa")

	if got := mustStatus(t, m, "prod").State; got != StateUnreachable {
		t.Errorf("network failure = %s, want unreachable", got)
	}
	if got := mustStatus(t, m, "qa").State; got != StateUnauthorized {
		t.Errorf("rejected credentials = %s, want unauthorized; the fix differs from unreachable", got)
	}
}

func TestForbiddenCountsAsUnauthorized(t *testing.T) {
	fc := newConnector()
	fc.errs["qa"] = apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "x", errors.New("nope"))
	m, _ := newManager(t, fc)

	_ = m.Connect(context.Background(), "qa")
	if got := mustStatus(t, m, "qa").State; got != StateUnauthorized {
		t.Fatalf("state = %s, want unauthorized", got)
	}
}

func TestExecPluginFailureCountsAsUnauthorized(t *testing.T) {
	fc := newConnector()
	// aws eks get-token exiting non-zero is not an apierrors type.
	fc.errs["prod"] = &AuthError{Err: errors.New("exec: aws: exit status 255")}
	m, _ := newManager(t, fc)

	_ = m.Connect(context.Background(), "prod")
	if got := mustStatus(t, m, "prod").State; got != StateUnauthorized {
		t.Fatalf("state = %s, want unauthorized so the UI prompts inline", got)
	}
}

func TestCircuitBreakerStopsHammering(t *testing.T) {
	fc := newConnector()
	fc.errs["prod"] = errors.New("dial tcp: i/o timeout")
	m, clk := newManager(t, fc)

	_ = m.Connect(context.Background(), "prod")
	if fc.attemptsFor("prod") != 1 {
		t.Fatalf("attempts = %d, want 1", fc.attemptsFor("prod"))
	}

	// Immediately retrying must not reach the connector.
	_ = m.Connect(context.Background(), "prod")
	if fc.attemptsFor("prod") != 1 {
		t.Fatalf("attempts = %d, want the breaker to suppress the retry", fc.attemptsFor("prod"))
	}
	if s := mustStatus(t, m, "prod"); s.RetryAfter.IsZero() {
		t.Error("status should tell the UI when a retry becomes possible")
	}

	clk.advance(90 * time.Second)
	_ = m.Connect(context.Background(), "prod")
	if fc.attemptsFor("prod") != 2 {
		t.Fatalf("attempts = %d, want a retry once the breaker window elapsed", fc.attemptsFor("prod"))
	}
}

func TestBreakerBacksOffGeometricallyAndCaps(t *testing.T) {
	m, _ := newManager(t, newConnector())
	prev := time.Duration(0)
	for i := 1; i <= 10; i++ {
		d := m.backoff(i)
		if d < prev {
			t.Fatalf("backoff(%d) = %s shrank from %s", i, d, prev)
		}
		if d > 8*time.Minute {
			t.Fatalf("backoff(%d) = %s exceeded the cap", i, d)
		}
		prev = d
	}
	if prev != 8*time.Minute {
		t.Errorf("backoff should reach the cap, got %s", prev)
	}
}

func TestSuccessResetsTheBreaker(t *testing.T) {
	fc := newConnector()
	fc.errs["qa"] = errors.New("boom")
	m, clk := newManager(t, fc)

	_ = m.Connect(context.Background(), "qa")
	clk.advance(2 * time.Minute)

	fc.mu.Lock()
	delete(fc.errs, "qa")
	fc.mu.Unlock()

	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	s := mustStatus(t, m, "qa")
	if s.Err != nil || !s.RetryAfter.IsZero() {
		t.Fatalf("a successful connect must clear the error and breaker, got %+v", s)
	}
}

// The core promise of per-context isolation.
func TestOneStuckClusterDoesNotBlockAnother(t *testing.T) {
	fc := newConnector()
	gate := make(chan struct{})
	fc.gates["prod"] = gate
	m, _ := newManager(t, fc)

	started := make(chan struct{})
	go func() {
		close(started)
		_ = m.Connect(context.Background(), "prod")
	}()
	<-started

	done := make(chan error, 1)
	go func() { done <- m.Connect(context.Background(), "qa") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("qa: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("qa blocked behind the stuck prod connection; connections must be independent")
	}

	if got := mustStatus(t, m, "qa").State; got != StateLive {
		t.Fatalf("qa = %s, want live", got)
	}
	if got := mustStatus(t, m, "prod").State; got != StateConnecting {
		t.Fatalf("prod = %s, want connecting", got)
	}
	close(gate)
}

func TestConcurrentConnectsAreRaceFree(t *testing.T) {
	fc := newConnector()
	m, _ := newManager(t, fc)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := []string{"qa", "stg", "prod"}[i%3]
			_ = m.Connect(context.Background(), name)
			m.Touch(name)
			_, _ = m.Status(name)
			_ = m.Statuses()
			m.ReapIdle()
		}(i)
	}
	wg.Wait()
}

func TestUnknownContextIsAnError(t *testing.T) {
	m, _ := newManager(t, newConnector())
	if err := m.Connect(context.Background(), "nope"); err == nil {
		t.Fatal("want an error for an unknown context")
	}
	if _, ok := m.Status("nope"); ok {
		t.Fatal("want no status for an unknown context")
	}
}

func TestStatusesCoverEveryContextInConnectOrder(t *testing.T) {
	m, _ := newManager(t, newConnector())
	got := m.Statuses()
	if len(got) != 3 {
		t.Fatalf("got %d statuses, want 3", len(got))
	}
	if got[0].Context != "stg" {
		t.Errorf("first = %q, want the current context", got[0].Context)
	}
}

func TestStateStringsAreStable(t *testing.T) {
	for s, want := range map[State]string{
		StateNeverConnected: "never-connected",
		StateConnecting:     "connecting",
		StateLive:           "live",
		StateStale:          "stale",
		StateUnreachable:    "unreachable",
		StateUnauthorized:   "unauthorized",
	} {
		if got := s.String(); got != want {
			t.Errorf("State(%d) = %q, want %q", s, got, want)
		}
	}
}

func TestConnectIsIdempotentWhenLive(t *testing.T) {
	fc := newConnector()
	m, _ := newManager(t, fc)

	for i := 0; i < 3; i++ {
		if err := m.Connect(context.Background(), "qa"); err != nil {
			t.Fatalf("Connect: %v", err)
		}
	}
	if fc.attemptsFor("qa") != 1 {
		t.Fatalf("attempts = %d, want 1: connecting a live context is a no-op", fc.attemptsFor("qa"))
	}
}

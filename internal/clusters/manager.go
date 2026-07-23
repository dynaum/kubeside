package clusters

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/dynaum/kubeside/internal/kubeconfig"
)

// Defaults from docs/04-multi-cluster.md.
const (
	DefaultIdleAfter   = 15 * time.Minute
	DefaultBreakerBase = 5 * time.Second
	DefaultBreakerMax  = 2 * time.Minute
)

// Clock is injected so lifecycle behaviour is tested without real time.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Session is a live connection's resources. Closing it drops informers while
// the manager keeps the last snapshot.
type Session interface{ Close() error }

// Connector opens a session for one kubeconfig context. The production
// implementation builds a REST config and starts informers; tests substitute a
// fake so no apiserver is required.
type Connector interface {
	Connect(ctx context.Context, kctx kubeconfig.Context) (Session, error)
}

// conn is one cluster's connection state. Every field is guarded by mu.
type conn struct {
	kctx kubeconfig.Context

	mu         sync.Mutex
	state      State
	lastLive   time.Time
	lastErr    error
	failures   int
	retryAfter time.Time
	session    Session
}

// Manager owns one connection per kubeconfig context.
//
// Connections are independent by construction: each has its own mutex, its own
// circuit breaker, and its own goroutine when connecting. A cluster behind a
// VPN that is switched off can never block a cluster that is reachable.
type Manager struct {
	cfg       *kubeconfig.Config
	connector Connector
	clock     Clock

	idleAfter   time.Duration
	breakerBase time.Duration
	breakerMax  time.Duration

	mu    sync.RWMutex
	conns map[string]*conn
}

// Options configures a Manager. Zero values take the documented defaults.
type Options struct {
	Clock       Clock
	IdleAfter   time.Duration
	BreakerBase time.Duration
	BreakerMax  time.Duration
}

// New builds a Manager over every context in the kubeconfig. No connection is
// opened: contexts connect lazily, on first use.
func New(cfg *kubeconfig.Config, connector Connector, opts Options) *Manager {
	m := &Manager{
		cfg:         cfg,
		connector:   connector,
		clock:       or[Clock](opts.Clock, realClock{}),
		idleAfter:   orDur(opts.IdleAfter, DefaultIdleAfter),
		breakerBase: orDur(opts.BreakerBase, DefaultBreakerBase),
		breakerMax:  orDur(opts.BreakerMax, DefaultBreakerMax),
		conns:       map[string]*conn{},
	}
	for _, kctx := range cfg.Contexts {
		m.conns[kctx.Name] = &conn{kctx: kctx, state: StateNeverConnected}
	}
	return m
}

func or[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

func orDur(v, fallback time.Duration) time.Duration {
	if v == 0 {
		return fallback
	}
	return v
}

// ConnectOrder returns context names with the current context first.
//
// Launch has no cache to replay, so the environment the developer actually
// works in must handshake first rather than waiting behind a slow prod cluster.
func (m *Manager) ConnectOrder() []string {
	names := make([]string, 0, len(m.cfg.Contexts))
	for _, c := range m.cfg.Contexts {
		names = append(names, c.Name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		return names[i] == m.cfg.Current && names[j] != m.cfg.Current
	})
	return names
}

// Connect opens a session for one context, respecting the circuit breaker.
// Calling it for an already-live context is a no-op.
func (m *Manager) Connect(ctx context.Context, name string) error {
	c, ok := m.conn(name)
	if !ok {
		return errUnknownContext(name)
	}

	c.mu.Lock()
	if c.state == StateLive {
		c.mu.Unlock()
		return nil
	}
	if now := m.clock.Now(); now.Before(c.retryAfter) {
		err := c.lastErr
		c.mu.Unlock()
		return err
	}
	c.state = StateConnecting
	kctx := c.kctx
	c.mu.Unlock()

	sess, err := m.connector.Connect(ctx, kctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	now := m.clock.Now()
	if err != nil {
		c.state = classify(err)
		c.lastErr = err
		c.failures++
		c.retryAfter = now.Add(m.backoff(c.failures))
		return err
	}
	c.session = sess
	c.state = StateLive
	c.lastLive = now
	c.lastErr = nil
	c.failures = 0
	c.retryAfter = time.Time{}
	return nil
}

// backoff grows the breaker window geometrically, capped, so a cluster that is
// simply off VPN is retried politely rather than hammered.
func (m *Manager) backoff(failures int) time.Duration {
	d := m.breakerBase
	for i := 1; i < failures && d < m.breakerMax; i++ {
		d *= 2
	}
	if d > m.breakerMax {
		d = m.breakerMax
	}
	return d
}

// Touch marks a context as in use, which defers its idle disconnect.
func (m *Manager) Touch(name string) {
	if c, ok := m.conn(name); ok {
		c.mu.Lock()
		if c.state == StateLive {
			c.lastLive = m.clock.Now()
		}
		c.mu.Unlock()
	}
}

// ReapIdle drops informers for live connections untouched beyond the idle
// window. The last snapshot is retained, so the connection becomes Stale
// rather than NeverConnected: the data is still shown, labelled with its age.
func (m *Manager) ReapIdle() []string {
	now := m.clock.Now()
	var reaped []string

	m.mu.RLock()
	all := make([]*conn, 0, len(m.conns))
	for _, c := range m.conns {
		all = append(all, c)
	}
	m.mu.RUnlock()

	for _, c := range all {
		c.mu.Lock()
		if c.state == StateLive && now.Sub(c.lastLive) >= m.idleAfter {
			if c.session != nil {
				_ = c.session.Close()
				c.session = nil
			}
			c.state = StateStale
			reaped = append(reaped, c.kctx.Name)
		}
		c.mu.Unlock()
	}
	sort.Strings(reaped)
	return reaped
}

// Status reports one connection without blocking on it.
func (m *Manager) Status(name string) (Status, bool) {
	c, ok := m.conn(name)
	if !ok {
		return Status{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	s := Status{
		Context:    c.kctx.Name,
		State:      c.state,
		LastLive:   c.lastLive,
		Err:        c.lastErr,
		RetryAfter: c.retryAfter,
	}
	if c.state == StateStale && !c.lastLive.IsZero() {
		s.Age = m.clock.Now().Sub(c.lastLive)
	}
	return s, true
}

// Statuses reports every connection, in connect order.
func (m *Manager) Statuses() []Status {
	order := m.ConnectOrder()
	out := make([]Status, 0, len(order))
	for _, n := range order {
		if s, ok := m.Status(n); ok {
			out = append(out, s)
		}
	}
	return out
}

// Close drops every session.
func (m *Manager) Close() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.conns {
		c.mu.Lock()
		if c.session != nil {
			_ = c.session.Close()
			c.session = nil
		}
		c.mu.Unlock()
	}
}

func (m *Manager) conn(name string) (*conn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conns[name]
	return c, ok
}

type unknownContextError string

func (e unknownContextError) Error() string { return "unknown kubeconfig context: " + string(e) }

func errUnknownContext(name string) error { return unknownContextError(name) }

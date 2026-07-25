// Package forward manages port-forwards into the cluster.
//
// The daily loop ends with "now let me hit the service", so a tool that stops
// short of that sends the developer back to a terminal for the last step. This
// closes it.
//
// Two rules shape the implementation. Every listener binds loopback only:
// forwarding a production database onto the office network would be a hole
// kubeside opened. And nothing survives the process: a tunnel into production
// left behind by a crashed UI is exactly what this tool must not leave running.
package forward

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// LocalAddress is where forwards bind. It is a constant rather than an option,
// for the same reason the web server's bind address is.
const LocalAddress = "127.0.0.1"

// DefaultStartTimeout bounds how long a caller waits for a tunnel to come up.
const DefaultStartTimeout = 15 * time.Second

// Forward states.
const (
	StateReady   = "ready"
	StateStopped = "stopped"
	StateFailed  = "failed"
)

// Target is what to forward to.
type Target struct {
	Context   string
	Namespace string
	Workload  string
	Pod       string
	// RemotePort is the container port. LocalPort is a request, and zero means
	// the operating system picks a free one.
	RemotePort int
	LocalPort  int
	// Environment and Risk travel with the forward so the UI can colour it. A
	// tunnel into production is a different thing from one into qa.
	Environment string
	Risk        string
}

func (t Target) key() string {
	return fmt.Sprintf("%s|%s/%s|%s|%d", t.Context, t.Namespace, t.Workload, t.Pod, t.RemotePort)
}

// Forward is one running tunnel.
type Forward struct {
	ID          string `json:"id"`
	Context     string `json:"context"`
	Namespace   string `json:"namespace"`
	Workload    string `json:"workload"`
	Pod         string `json:"pod"`
	RemotePort  int    `json:"remotePort"`
	LocalPort   int    `json:"localPort"`
	Address     string `json:"address"`
	State       string `json:"state"`
	Environment string `json:"environment,omitempty"`
	Risk        string `json:"risk,omitempty"`
	StartedAt   string `json:"startedAt"`
	Error       string `json:"error,omitempty"`
}

// Tunnel opens one port-forward and holds it until the context is cancelled.
// Keeping it an interface lets the lifecycle be tested without an apiserver.
type Tunnel interface {
	Open(ctx context.Context, t Target, ready func(localPort int)) error
}

type entry struct {
	forward Forward
	cancel  context.CancelFunc
}

// Manager owns every live forward.
type Manager struct {
	tunnel       Tunnel
	startTimeout time.Duration
	now          func() time.Time

	mu       sync.Mutex
	entries  map[string]*entry
	byTarget map[string]string
	seq      int
}

// New builds a manager. Nothing is opened until Start.
func New(t Tunnel) *Manager {
	return &Manager{
		tunnel:       t,
		startTimeout: DefaultStartTimeout,
		now:          time.Now,
		entries:      map[string]*entry{},
		byTarget:     map[string]string{},
	}
}

// Start opens a forward, or hands back the one already serving this target.
//
// Asking twice for the same service should not produce two tunnels on two
// ports, because then half the developer's terminal history points at a port
// that is no longer the one.
func (m *Manager) Start(ctx context.Context, t Target) (Forward, error) {
	m.mu.Lock()
	if id, ok := m.byTarget[t.key()]; ok {
		if e := m.entries[id]; e != nil {
			f := e.forward
			m.mu.Unlock()
			return f, nil
		}
	}
	m.seq++
	id := fmt.Sprintf("fwd-%d", m.seq)
	m.mu.Unlock()

	// The tunnel outlives this call: the request that opened it returns as soon
	// as the listener is up, and the forward keeps running until it is stopped.
	runCtx, cancel := context.WithCancel(context.Background())

	ready := make(chan int, 1)
	failed := make(chan error, 1)
	go func() {
		err := m.tunnel.Open(runCtx, t, func(local int) { ready <- local })
		if err != nil && runCtx.Err() == nil {
			failed <- err
		}
		m.forget(id)
	}()

	select {
	case local := <-ready:
		f := Forward{
			ID: id, Context: t.Context, Namespace: t.Namespace, Workload: t.Workload,
			Pod: t.Pod, RemotePort: t.RemotePort, LocalPort: local,
			Address:     fmt.Sprintf("%s:%d", LocalAddress, local),
			State:       StateReady,
			Environment: t.Environment, Risk: t.Risk,
			StartedAt: m.now().UTC().Format(time.RFC3339),
		}
		m.mu.Lock()
		m.entries[id] = &entry{forward: f, cancel: cancel}
		m.byTarget[t.key()] = id
		m.mu.Unlock()
		return f, nil

	case err := <-failed:
		cancel()
		return Forward{}, err

	case <-ctx.Done():
		cancel()
		return Forward{}, ctx.Err()

	case <-time.After(m.startTimeout):
		// A tunnel that has not come up in this long is not coming up, and a
		// request that hangs is worse than one that fails.
		cancel()
		return Forward{}, fmt.Errorf("port-forward to %s:%d did not come up within %s", t.Pod, t.RemotePort, m.startTimeout)
	}
}

// List returns every live forward, oldest first.
func (m *Manager) List() []Forward {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Forward, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.forward)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Stop closes one forward.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("no forward %q is running", id)
	}
	delete(m.entries, id)
	for key, held := range m.byTarget {
		if held == id {
			delete(m.byTarget, key)
		}
	}
	m.mu.Unlock()

	e.cancel()
	return nil
}

// StopAll closes every forward. It runs when kubeside exits, so no tunnel
// outlives the process that opened it.
func (m *Manager) StopAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.entries))
	for _, e := range m.entries {
		cancels = append(cancels, e.cancel)
	}
	m.entries = map[string]*entry{}
	m.byTarget = map[string]string{}
	m.mu.Unlock()

	for _, c := range cancels {
		c()
	}
}

// forget drops a forward whose tunnel ended on its own, which is what happens
// when the pod goes away underneath it.
func (m *Manager) forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
	for key, held := range m.byTarget {
		if held == id {
			delete(m.byTarget, key)
		}
	}
}

package api

import (
	"sync"
	"time"
)

// Reconstruction reads a handful of collections per call, so opening the same
// app twice in a minute should not re-read them twice. Results are memoized for
// a short window rather than for the session: until live extension lands
// (issue #17), a cache that never expires would hide a rollout that happened
// while the window was open, and a timeline that quietly lags is worse than one
// that costs a read.
const memoTTL = 30 * time.Second

type memoEntry[T any] struct {
	value T
	at    time.Time
}

// memo is a tiny TTL cache. Nothing here reaches disk: it dies with the
// process, like everything else kubeside observes.
type memo[T any] struct {
	mu      sync.Mutex
	entries map[string]memoEntry[T]
	ttl     time.Duration
	now     func() time.Time
}

func newMemo[T any](ttl time.Duration) *memo[T] {
	return &memo[T]{entries: map[string]memoEntry[T]{}, ttl: ttl, now: time.Now}
}

// Do returns the cached value for key, computing it when absent or stale.
func (m *memo[T]) Do(key string, compute func() (T, error)) (T, error) {
	m.mu.Lock()
	e, ok := m.entries[key]
	fresh := ok && m.now().Sub(e.at) < m.ttl
	m.mu.Unlock()

	if fresh {
		return e.value, nil
	}

	v, err := compute()
	if err != nil {
		var zero T
		return zero, err
	}

	m.mu.Lock()
	m.entries[key] = memoEntry[T]{value: v, at: m.now()}
	m.mu.Unlock()
	return v, nil
}

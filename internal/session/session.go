// Package session holds what kubeside observed while it was running.
//
// kubeside writes nothing to disk, so retention did not disappear: it moved to
// RAM with a much smaller budget. That budget is enforced from the first
// release rather than discovered at 400MB on somebody's laptop.
//
// Eviction is oldest-first and visible. A buffer that quietly drops the
// beginning of a session turns a gap into an apparent quiet period, which is
// the one failure this product cannot afford.
package session

import (
	"sort"
	"sync"
	"time"

	"github.com/dynaum/kubeside/internal/timeline"
)

// Defaults, from docs/05-architecture.md.
const (
	// DefaultMaxEntriesPerApp bounds one app's live history.
	DefaultMaxEntriesPerApp = 2000
	// DefaultMaxBytesPerApp keeps one chatty app from spending the whole
	// budget before the global cap notices.
	DefaultMaxBytesPerApp = 4 << 20 // 4MB
	// DefaultMaxBytesTotal is the budget across every app.
	DefaultMaxBytesTotal = 100 << 20 // 100MB
)

// Limits configure the buffers. Zero values take the defaults, so a caller
// cannot accidentally opt out of the cap.
type Limits struct {
	MaxEntriesPerApp int
	MaxBytesPerApp   int
	MaxBytesTotal    int
}

func (l Limits) withDefaults() Limits {
	if l.MaxEntriesPerApp <= 0 {
		l.MaxEntriesPerApp = DefaultMaxEntriesPerApp
	}
	if l.MaxBytesPerApp <= 0 {
		l.MaxBytesPerApp = DefaultMaxBytesPerApp
	}
	if l.MaxBytesTotal <= 0 {
		l.MaxBytesTotal = DefaultMaxBytesTotal
	}
	return l
}

// buffer is one app's ring.
type buffer struct {
	// entries are oldest first, which is the order eviction works in.
	entries []timeline.Entry
	bytes   int
	evicted int
	started time.Time
}

// Store holds one buffer per app, under a global byte budget.
type Store struct {
	mu      sync.Mutex
	limits  Limits
	buffers map[string]*buffer
	bytes   int
	now     func() time.Time
}

// New builds a store. Nothing it holds ever reaches disk.
func New(l Limits) *Store {
	return &Store{limits: l.withDefaults(), buffers: map[string]*buffer{}, now: time.Now}
}

// Record adds one observation to an app's buffer.
//
// A repeat of the entry already at the head is dropped: a caller that observes
// the same state twice is reporting a poll, not a change.
func (s *Store) Record(app string, e timeline.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.buffers[app]
	if b == nil {
		b = &buffer{started: s.now()}
		s.buffers[app] = b
	}
	if n := len(b.entries); n > 0 && sameObservation(b.entries[n-1], e) {
		return
	}

	size := sizeOf(e)
	b.entries = append(b.entries, e)
	b.bytes += size
	s.bytes += size

	s.trimApp(b)
	s.trimTotal()
}

// Entries returns one app's live history, newest first.
func (s *Store) Entries(app string) []timeline.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.buffers[app]
	if b == nil {
		return nil
	}
	out := make([]timeline.Entry, len(b.entries))
	for i, e := range b.entries {
		out[len(b.entries)-1-i] = e
	}
	return out
}

// Horizon says where this app's live history begins, and why it begins there.
//
// Two different facts, two different sentences: eviction is a cut in something
// that was known, and a session start is simply the moment kubeside began
// watching. Rendering either as silence would mislead.
func (s *Store) Horizon(app string) *timeline.Horizon {
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.buffers[app]
	if b == nil || len(b.entries) == 0 {
		return nil
	}
	oldest := b.entries[0].At

	if b.evicted > 0 {
		return &timeline.Horizon{
			At:     oldest,
			Source: "session",
			Pruned: true,
			Reason: "earlier observations from this session were evicted to stay inside kubeside's memory budget",
		}
	}
	return &timeline.Horizon{
		At:     b.started,
		Source: "session",
		Pruned: false,
		Reason: "kubeside started watching here; anything before this comes from the cluster's own history",
	}
}

// Bytes is the total held across every app, which is what the global budget
// bounds.
func (s *Store) Bytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// trimApp enforces one app's caps. Callers hold the lock.
func (s *Store) trimApp(b *buffer) {
	for len(b.entries) > s.limits.MaxEntriesPerApp || b.bytes > s.limits.MaxBytesPerApp {
		if len(b.entries) == 0 {
			return
		}
		s.evictOldest(b)
	}
}

// trimTotal enforces the global budget by evicting the oldest observation
// anywhere. One noisy app must not be able to spend everybody's budget, and the
// oldest thing in memory is the least useful thing in it.
func (s *Store) trimTotal() {
	for s.bytes > s.limits.MaxBytesTotal {
		var victim *buffer
		var oldest time.Time
		for _, b := range s.buffers {
			if len(b.entries) == 0 {
				continue
			}
			if victim == nil || b.entries[0].At.Before(oldest) {
				victim, oldest = b, b.entries[0].At
			}
		}
		if victim == nil {
			return
		}
		s.evictOldest(victim)
	}
}

func (s *Store) evictOldest(b *buffer) {
	size := sizeOf(b.entries[0])
	b.entries = b.entries[1:]
	b.bytes -= size
	s.bytes -= size
	b.evicted++
}

// sizeOf approximates an entry's footprint. Exactness is not the point: the
// budget exists to bound growth, and counting the strings that dominate an
// entry is enough to do that.
func sizeOf(e timeline.Entry) int {
	const overhead = 96 // struct, time, and map/slice headers
	return overhead + len(e.Kind) + len(e.Title) + len(e.Detail) + len(e.Source) +
		len(e.Revision) + len(e.Image) + len(e.Pod) + len(e.Actor.Kind) + len(e.Actor.Name)
}

func sameObservation(a, b timeline.Entry) bool {
	return a.Kind == b.Kind && a.Title == b.Title && a.Detail == b.Detail && a.At.Equal(b.At)
}

// Merge folds live observations into reconstructed history, newest first.
func Merge(reconstructed, live []timeline.Entry) []timeline.Entry {
	all := make([]timeline.Entry, 0, len(reconstructed)+len(live))
	all = append(all, reconstructed...)
	all = append(all, live...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	return all
}

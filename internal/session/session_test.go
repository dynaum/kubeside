package session

import (
	"strings"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/timeline"
)

func entry(when time.Time, title string) timeline.Entry {
	return timeline.Entry{At: when, Kind: timeline.KindDeploy, Title: title, Source: "session"}
}

func base() time.Time { return time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC) }

func TestRecordKeepsEntriesNewestFirst(t *testing.T) {
	s := New(Limits{})
	s.Record("qa|team-a/checkout", entry(base(), "first"))
	s.Record("qa|team-a/checkout", entry(base().Add(time.Minute), "second"))

	got := s.Entries("qa|team-a/checkout")
	if len(got) != 2 || got[0].Title != "second" {
		t.Fatalf("entries = %+v, want newest first", got)
	}
}

func TestAppsAreIndependent(t *testing.T) {
	s := New(Limits{})
	s.Record("qa|team-a/checkout", entry(base(), "checkout"))
	s.Record("qa|team-b/search", entry(base(), "search"))

	if len(s.Entries("qa|team-a/checkout")) != 1 || len(s.Entries("qa|team-b/search")) != 1 {
		t.Fatal("one app's entries leaked into another's buffer")
	}
}

// Retention moved from disk to RAM, so the entry cap is enforced from the first
// release rather than discovered at 400MB.
func TestEntryCapEvictsOldestFirst(t *testing.T) {
	s := New(Limits{MaxEntriesPerApp: 3})
	for i := 0; i < 6; i++ {
		s.Record("app", entry(base().Add(time.Duration(i)*time.Minute), string(rune('a'+i))))
	}

	got := s.Entries("app")
	if len(got) != 3 {
		t.Fatalf("entries = %d, want the cap", len(got))
	}
	if got[0].Title != "f" || got[2].Title != "d" {
		t.Errorf("kept %v, want the newest three", titles(got))
	}
}

func TestByteCapEvictsOldestFirst(t *testing.T) {
	long := strings.Repeat("x", 400)
	s := New(Limits{MaxBytesPerApp: 1000})
	for i := 0; i < 5; i++ {
		s.Record("app", entry(base().Add(time.Duration(i)*time.Minute), long+string(rune('a'+i))))
	}

	got := s.Entries("app")
	if len(got) == 0 || len(got) > 2 {
		t.Fatalf("entries = %d, want only what fits in the byte budget", len(got))
	}
	if !strings.HasSuffix(got[0].Title, "e") {
		t.Errorf("newest kept = %q, want the last recorded", got[0].Title[len(got[0].Title)-1:])
	}
}

// Eviction is visible rather than silent. A buffer that quietly drops the
// beginning of a session turns a gap into an apparent quiet period.
func TestEvictionIsVisible(t *testing.T) {
	s := New(Limits{MaxEntriesPerApp: 2})
	for i := 0; i < 5; i++ {
		s.Record("app", entry(base().Add(time.Duration(i)*time.Minute), "e"))
	}

	h := s.Horizon("app")
	if h == nil {
		t.Fatal("no horizon after eviction")
	}
	if !strings.Contains(h.Reason, "evicted") {
		t.Errorf("reason = %q, should say entries were evicted", h.Reason)
	}
	if !h.Pruned {
		t.Error("an evicted horizon is a cut, not a beginning")
	}
	if !h.At.Equal(base().Add(3 * time.Minute)) {
		t.Errorf("horizon at %v, want the oldest surviving entry", h.At)
	}
}

// Nothing evicted means the horizon is where kubeside started watching, which
// is a different fact and gets a different sentence.
func TestHorizonWithoutEvictionIsTheSessionStart(t *testing.T) {
	s := New(Limits{})
	s.Record("app", entry(base(), "one"))

	h := s.Horizon("app")
	if h == nil {
		t.Fatal("no horizon")
	}
	if h.Pruned {
		t.Error("nothing was evicted; this is a beginning, not a cut")
	}
	if !strings.Contains(h.Reason, "kubeside started") {
		t.Errorf("reason = %q, should mark where the session began", h.Reason)
	}
}

func TestNoEntriesMeansNoHorizon(t *testing.T) {
	s := New(Limits{})
	if h := s.Horizon("never-seen"); h != nil {
		t.Fatalf("horizon = %+v, want none for an app with no observations", h)
	}
}

// The global budget is what keeps thirty watched apps from costing thirty
// per-app budgets. One noisy app must not be able to spend everybody's.
func TestGlobalBudgetEvictsAcrossApps(t *testing.T) {
	long := strings.Repeat("x", 400)
	s := New(Limits{MaxBytesTotal: 2000, MaxBytesPerApp: 100_000})

	for i := 0; i < 4; i++ {
		s.Record("noisy", entry(base().Add(time.Duration(i)*time.Minute), long))
	}
	s.Record("quiet", entry(base().Add(time.Hour), "small"))

	if s.Bytes() > 2000 {
		t.Fatalf("total bytes = %d, over the budget", s.Bytes())
	}
	// The app that just spoke keeps its entry; the budget comes out of the
	// oldest observations, wherever they are.
	if len(s.Entries("quiet")) != 1 {
		t.Errorf("the quiet app lost its only entry to another app's noise")
	}
}

func TestDefaultsAreEnforcedWithoutConfiguration(t *testing.T) {
	s := New(Limits{})
	if s.limits.MaxEntriesPerApp <= 0 || s.limits.MaxBytesPerApp <= 0 || s.limits.MaxBytesTotal <= 0 {
		t.Fatalf("limits = %+v; the cap must exist from the first release", s.limits)
	}
	if s.limits.MaxBytesTotal != DefaultMaxBytesTotal {
		t.Errorf("total budget = %d, want the documented 100MB", s.limits.MaxBytesTotal)
	}
}

// Recording the same observation twice is a bug in the caller, not history.
func TestIdenticalConsecutiveEntriesAreNotDuplicated(t *testing.T) {
	s := New(Limits{})
	e := entry(base(), "checkout became failed")
	s.Record("app", e)
	s.Record("app", e)

	if got := s.Entries("app"); len(got) != 1 {
		t.Fatalf("entries = %+v, want the repeat ignored", titles(got))
	}
}

func titles(entries []timeline.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Title)
	}
	return out
}

package api

import (
	"errors"
	"testing"
	"time"
)

func TestMemoComputesOnceWithinTheWindow(t *testing.T) {
	m := newMemo[int](time.Minute)
	calls := 0
	compute := func() (int, error) { calls++; return 7, nil }

	for i := 0; i < 3; i++ {
		v, err := m.Do("a", compute)
		if err != nil || v != 7 {
			t.Fatalf("Do = %v, %v", v, err)
		}
	}
	if calls != 1 {
		t.Errorf("computed %d times, want 1", calls)
	}
}

func TestMemoRecomputesWhenStale(t *testing.T) {
	m := newMemo[int](time.Minute)
	now := time.Unix(0, 0)
	m.now = func() time.Time { return now }
	calls := 0
	compute := func() (int, error) { calls++; return calls, nil }

	if _, err := m.Do("a", compute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	v, _ := m.Do("a", compute)

	if v != 2 || calls != 2 {
		t.Errorf("value = %d after %d calls; a stale entry must be recomputed", v, calls)
	}
}

func TestMemoKeysAreIndependent(t *testing.T) {
	m := newMemo[string](time.Minute)
	a, _ := m.Do("a", func() (string, error) { return "one", nil })
	b, _ := m.Do("b", func() (string, error) { return "two", nil })
	if a != "one" || b != "two" {
		t.Errorf("a = %q, b = %q", a, b)
	}
}

// A failed read must not be cached: the next open should try again rather than
// serve the failure for half a minute.
func TestMemoDoesNotCacheFailures(t *testing.T) {
	m := newMemo[int](time.Minute)
	calls := 0
	_, err := m.Do("a", func() (int, error) { calls++; return 0, errors.New("forbidden") })
	if err == nil {
		t.Fatal("want the error through")
	}
	if _, err := m.Do("a", func() (int, error) { calls++; return 5, nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("computed %d times; the failure was cached", calls)
	}
}

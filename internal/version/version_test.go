package version

import "testing"

func TestStringIncludesAllFields(t *testing.T) {
	Version, Commit, Date = "1.2.3", "abc1234", "2026-07-22"
	t.Cleanup(func() { Version, Commit, Date = "dev", "none", "unknown" })

	got := String()
	want := "1.2.3 (abc1234, 2026-07-22)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringDefaultsAreSafe(t *testing.T) {
	if String() != "dev (none, unknown)" {
		t.Fatalf("unstamped build should report dev, got %q", String())
	}
}

package logs

import (
	"strings"
	"testing"
	"time"
)

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func TestParseLineSplitsTheKubeletTimestamp(t *testing.T) {
	when, text, ok := ParseLine("2026-07-24T10:00:00.123456789Z starting server on :8080")
	if !ok {
		t.Fatal("a timestamped line was not recognized")
	}
	if text != "starting server on :8080" {
		t.Errorf("text = %q", text)
	}
	if !when.Equal(ts(t, "2026-07-24T10:00:00.123456789Z")) {
		t.Errorf("time = %v", when)
	}
}

// A line the kubelet did not stamp keeps its text intact. Guessing a timestamp
// would place it wrongly in a merge; saying so lets the caller decide.
func TestParseLineWithoutATimestamp(t *testing.T) {
	when, text, ok := ParseLine("panic: runtime error")
	if ok {
		t.Fatal("an unstamped line was reported as stamped")
	}
	if text != "panic: runtime error" {
		t.Errorf("text = %q; the whole line must survive", text)
	}
	if !when.IsZero() {
		t.Errorf("time = %v, want zero", when)
	}
}

func TestParseLineTolerantOfEmptyText(t *testing.T) {
	_, text, ok := ParseLine("2026-07-24T10:00:00Z ")
	if !ok || text != "" {
		t.Fatalf("text = %q, ok = %v; a blank log line is still a log line", text, ok)
	}
}

func TestScanEmitsOneLinePerRecord(t *testing.T) {
	in := strings.NewReader(
		"2026-07-24T10:00:00Z first\n" +
			"2026-07-24T10:00:01Z second\n" +
			"2026-07-24T10:00:02Z third without newline")

	var got []Line
	if err := Scan(in, Meta{Pod: "checkout-1", Container: "app"}, func(l Line) { got = append(got, l) }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("lines = %d, want 3 including the unterminated last one", len(got))
	}
	if got[0].Pod != "checkout-1" || got[0].Container != "app" {
		t.Errorf("meta lost: %+v", got[0])
	}
	if got[2].Text != "third without newline" {
		t.Errorf("last line = %q", got[2].Text)
	}
}

// A megabyte of JSON on one line is a real thing applications do. It must not
// kill the stream, and the truncation must be visible rather than silent.
func TestScanTruncatesAnEnormousLine(t *testing.T) {
	huge := strings.Repeat("x", MaxLineBytes*2)
	in := strings.NewReader("2026-07-24T10:00:00Z " + huge + "\n2026-07-24T10:00:01Z after\n")

	var got []Line
	if err := Scan(in, Meta{Pod: "p", Container: "c"}, func(l Line) { got = append(got, l) }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("lines = %d, want the huge line plus the one after it", len(got))
	}
	if !got[0].Truncated {
		t.Error("the oversized line is not marked truncated")
	}
	if len(got[0].Text) > MaxLineBytes {
		t.Errorf("text kept %d bytes, want at most %d", len(got[0].Text), MaxLineBytes)
	}
	if got[1].Text != "after" {
		t.Errorf("the stream did not recover: %q", got[1].Text)
	}
}

func TestBufferKeepsTheNewestLinesAndCountsTheRest(t *testing.T) {
	b := NewBuffer(3)
	for i := 0; i < 10; i++ {
		b.Add(Line{Time: ts(t, "2026-07-24T10:00:00Z").Add(time.Duration(i) * time.Second), Text: string(rune('a' + i))})
	}

	lines, dropped := b.Snapshot()
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want the cap", len(lines))
	}
	if lines[0].Text != "h" || lines[2].Text != "j" {
		t.Errorf("kept %v, want the newest three", texts(lines))
	}
	// Silently losing lines would make a chatty workload look quiet.
	if dropped != 7 {
		t.Errorf("dropped = %d, want 7", dropped)
	}
}

// Replicas do not arrive in lockstep. A line stamped earlier than one already
// buffered belongs before it, so the merged view reads in real time order.
func TestBufferInsertsOutOfOrderLinesInPlace(t *testing.T) {
	b := NewBuffer(10)
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:02Z"), Pod: "a", Text: "second"})
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:01Z"), Pod: "b", Text: "first"})
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:03Z"), Pod: "a", Text: "third"})

	lines, _ := b.Snapshot()
	if got := texts(lines); got != "first second third" {
		t.Fatalf("order = %q", got)
	}
	for _, l := range lines {
		if l.Late {
			t.Errorf("line %q marked late; it was inside the reorder window", l.Text)
		}
	}
}

// A line that turns up long after its moment cannot be silently slotted into
// the past: the reader has already scrolled past it. It lands at the end and
// says it is out of order.
func TestBufferMarksAVeryLateLineInsteadOfReordering(t *testing.T) {
	b := NewBuffer(10)
	b.endSettle()
	base := ts(t, "2026-07-24T10:00:00Z")
	b.Add(Line{Time: base.Add(time.Minute), Text: "now"})
	b.Add(Line{Time: base, Text: "ancient"})

	lines, _ := b.Snapshot()
	if texts(lines) != "now ancient" {
		t.Fatalf("order = %q, want the late line appended", texts(lines))
	}
	if !lines[1].Late {
		t.Error("the late line is not marked")
	}
}

// An unstamped line belongs immediately after the line it followed, not at the
// epoch. A stack trace must not be flung to the top of the window.
func TestBufferKeepsUnstampedLinesWithTheirPredecessor(t *testing.T) {
	b := NewBuffer(10)
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:01Z"), Pod: "a", Text: "panic: boom"})
	b.Add(Line{Pod: "a", Text: "  goroutine 1 [running]:"})
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:02Z"), Pod: "b", Text: "later"})

	lines, _ := b.Snapshot()
	if got := texts(lines); got != "panic: boom   goroutine 1 [running]: later" {
		t.Fatalf("order = %q", got)
	}
}

func TestBufferSinceReturnsOnlyNewLines(t *testing.T) {
	b := NewBuffer(10)
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:01Z"), Text: "one"})
	_, cursor := b.Snapshot()
	_ = cursor
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:02Z"), Text: "two"})

	fresh, _ := b.Since(1)
	if len(fresh) != 1 || fresh[0].Text != "two" {
		t.Fatalf("since = %v, want only the new line", texts(fresh))
	}
}

// Sidecar output drowns application logs on a mesh cluster. Hidden by default,
// never dropped: the caller can ask for them.
func TestMeshSidecarsAreHiddenByDefault(t *testing.T) {
	for _, name := range []string{"istio-proxy", "linkerd-proxy", "envoy", "istio-init", "linkerd-init"} {
		if !HiddenByDefault(name) {
			t.Errorf("%s should be hidden by default", name)
		}
	}
	for _, name := range []string{"app", "checkout", "worker", "envoy-config-reloader"} {
		if HiddenByDefault(name) {
			t.Errorf("%s is application output and must not be hidden", name)
		}
	}
}

func texts(lines []Line) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return strings.Join(out, " ")
}

// Opening a workload replays each replica's scrollback at once, and those
// backlogs can be hours apart. The first screen must read in real time order,
// not in the order the kubelets answered.
func TestBufferMergesTheInitialBacklogByTimestamp(t *testing.T) {
	b := NewBuffer(10)

	// The second replica answers first, with much older output.
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:00Z"), Pod: "b", Text: "recent"})
	b.Add(Line{Time: ts(t, "2026-07-24T04:00:00Z"), Pod: "a", Text: "ancient"})
	b.Add(Line{Time: ts(t, "2026-07-24T04:00:01Z"), Pod: "a", Text: "old"})

	lines, _ := b.Snapshot()
	if got := texts(lines); got != "ancient old recent" {
		t.Fatalf("order = %q, want the backlog merged by time", got)
	}
	for _, l := range lines {
		if l.Late {
			t.Errorf("backlog line %q marked late; nobody has read it yet", l.Text)
		}
	}
}

// Once the backlog has settled, the live rule applies again: a line whose
// moment has passed is appended and says so.
func TestBufferReturnsToTheLiveRuleAfterSettling(t *testing.T) {
	b := NewBuffer(10)
	b.Add(Line{Time: ts(t, "2026-07-24T10:00:00Z"), Text: "first"})
	b.endSettle()
	b.Add(Line{Time: ts(t, "2026-07-24T04:00:00Z"), Text: "stale arrival"})

	lines, _ := b.Snapshot()
	if texts(lines) != "first stale arrival" || !lines[1].Late {
		t.Fatalf("lines = %+v, want the late line appended and marked", lines)
	}
}

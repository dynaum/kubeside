package exec

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeExecutor stands in for the cluster: it echoes whatever stdin sends and
// records the sizes it was told about.
type fakeExecutor struct {
	mu      sync.Mutex
	target  Target
	sizes   []Size
	fail    error
	started bool
}

func (f *fakeExecutor) Run(ctx context.Context, t Target, io Streams) error {
	f.mu.Lock()
	f.target = t
	f.started = true
	err := f.fail
	f.mu.Unlock()
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case size, ok := <-io.Resize:
				if !ok {
					return
				}
				f.mu.Lock()
				f.sizes = append(f.sizes, size)
				f.mu.Unlock()
			}
		}
	}()

	buf := make([]byte, 64)
	for {
		n, err := io.Stdin.Read(buf)
		if n > 0 {
			if _, werr := io.Stdout.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == context.Canceled || errors.Is(err, io2EOF) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

var io2EOF = errorString("EOF")

type errorString string

func (e errorString) Error() string { return string(e) }

func (f *fakeExecutor) seenSizes() []Size {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Size, len(f.sizes))
	copy(out, f.sizes)
	return out
}

func target() Target {
	return Target{
		Context: "qa1", Namespace: "team-a", Pod: "checkout-1", Container: "app",
		Command: nil,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestASessionCarriesInputToTheContainerAndOutputBack(t *testing.T) {
	f := &fakeExecutor{}
	s := New(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out strings.Builder
	var mu sync.Mutex
	done := s.Start(ctx, target(), func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		out.Write(b)
	})

	s.Write([]byte("whoami\n"))
	waitFor(t, "the echo to come back", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), "whoami")
	})

	s.Close()
	<-done
}

// A shell that does not know the window size wraps every line wrongly, which
// makes the terminal unusable rather than merely ugly.
func TestResizeReachesTheContainer(t *testing.T) {
	f := &fakeExecutor{}
	s := New(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := s.Start(ctx, target(), func([]byte) {})

	s.Resize(Size{Cols: 120, Rows: 40})
	waitFor(t, "the size to arrive", func() bool { return len(f.seenSizes()) > 0 })

	if got := f.seenSizes()[0]; got.Cols != 120 || got.Rows != 40 {
		t.Fatalf("size = %+v", got)
	}
	s.Close()
	<-done
}

// The default command is a shell that exists in more images than bash does, and
// falling back inside one invocation beats failing on an alpine container.
func TestDefaultCommandFallsBackFromBash(t *testing.T) {
	got := CommandFor(nil)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "bash") || !strings.Contains(joined, "sh") {
		t.Fatalf("command = %v, want a shell with a fallback", got)
	}
	// A failed exec replaces the shell with nothing and exits 127, so a
	// fallback placed after one never runs. The shell has to be tested for
	// before it is exec'd, which is what this asserts and what the first
	// version against an alpine image got wrong.
	probe := strings.Index(joined, "command -v")
	firstExec := strings.Index(joined, "exec ")
	if probe < 0 || probe > firstExec {
		t.Fatalf("command = %v; the shell must be tested for before it is exec'd", got)
	}
}

func TestAnExplicitCommandIsUsedAsGiven(t *testing.T) {
	got := CommandFor([]string{"/bin/zsh", "-l"})
	if len(got) != 2 || got[0] != "/bin/zsh" {
		t.Fatalf("command = %v, want exactly what was asked for", got)
	}
}

// A session that cannot start says why rather than leaving a blank terminal.
func TestAFailedSessionReportsTheReason(t *testing.T) {
	f := &fakeExecutor{fail: errors.New("container app is not running")}
	s := New(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := s.Start(ctx, target(), func([]byte) {})
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v, want the reason carried back", err)
	}
}

// Closing the tab must end the session in the cluster, not leave a shell
// running with nobody attached.
func TestClosingEndsTheSession(t *testing.T) {
	f := &fakeExecutor{}
	s := New(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := s.Start(ctx, target(), func([]byte) {})

	waitFor(t, "the session to start", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.started
	})
	s.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("closing did not end the session")
	}
}

// Writing after close is what a racing browser does; it must not panic.
func TestWritingAfterCloseIsHarmless(t *testing.T) {
	s := New(&fakeExecutor{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := s.Start(ctx, target(), func([]byte) {})

	s.Close()
	<-done
	s.Write([]byte("still typing"))
	s.Resize(Size{Cols: 80, Rows: 24})
}

// The target names one container of one pod in one context. There is no shape
// here that could address two.
func TestTargetIsAlwaysOneContainer(t *testing.T) {
	f := &fakeExecutor{}
	s := New(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := s.Start(ctx, target(), func([]byte) {})
	waitFor(t, "the session to start", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.started
	})

	f.mu.Lock()
	got := f.target
	f.mu.Unlock()
	if got.Pod != "checkout-1" || got.Container != "app" || got.Context != "qa1" {
		t.Fatalf("target = %+v", got)
	}

	s.Close()
	<-done
}

var _ = io.EOF

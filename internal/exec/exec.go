// Package exec runs an interactive shell in a container, proxied through this
// process.
//
// This is the first genuinely destructive capability kubeside has, which is why
// the guardrails and the permission resolver landed before it. Nothing here
// decides whether a session may start: the caller checks the environment's write
// policy and asks the cluster for pods/exec, and only then opens a session.
//
// The browser never talks to an apiserver. Keystrokes arrive over the local
// websocket, cross into a SPDY stream here, and the credentials stay in this
// process, exactly as every other read does.
package exec

import (
	"context"
	"io"
	"sync"
)

// Size is a terminal window. A shell that does not know it wraps every line
// wrongly, which makes a terminal unusable rather than merely ugly.
type Size struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Target names exactly one container of one pod in one context. There is no
// shape here that could address two.
type Target struct {
	Context   string
	Namespace string
	Pod       string
	Container string
	Command   []string
}

// Streams is what an executor is handed.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Resize <-chan Size
}

// Executor runs one session against a cluster. Keeping it an interface lets the
// plumbing be tested without an apiserver, which matters more here than
// anywhere else in the product.
type Executor interface {
	Run(ctx context.Context, t Target, streams Streams) error
}

// Session is one running shell.
type Session struct {
	executor Executor

	mu     sync.Mutex
	stdin  *io.PipeWriter
	resize chan Size
	closed bool
}

// New builds a session. Nothing runs until Start.
func New(e Executor) *Session {
	return &Session{executor: e}
}

// Start opens the session and streams output to onOutput until it ends.
//
// The returned channel carries the session's ending: nil when the shell exited,
// or the reason it could not start. A blank terminal that never says why is the
// worst outcome available here.
func (s *Session) Start(ctx context.Context, t Target, onOutput func([]byte)) <-chan error {
	reader, writer := io.Pipe()
	resize := make(chan Size, 4)

	s.mu.Lock()
	s.stdin = writer
	s.resize = resize
	s.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		defer close(done)
		out := writerFunc(onOutput)
		err := s.executor.Run(ctx, Target{
			Context: t.Context, Namespace: t.Namespace, Pod: t.Pod,
			Container: t.Container, Command: CommandFor(t.Command),
		}, Streams{Stdin: reader, Stdout: out, Stderr: out, Resize: resize})
		s.Close()
		done <- err
	}()
	return done
}

// Write sends keystrokes to the container.
func (s *Session) Write(b []byte) {
	s.mu.Lock()
	w, closed := s.stdin, s.closed
	s.mu.Unlock()
	if closed || w == nil {
		// A browser that keeps typing into a closed session is a race, not a
		// bug worth crashing over.
		return
	}
	_, _ = w.Write(b)
}

// Resize tells the shell how big its window is.
func (s *Session) Resize(size Size) {
	s.mu.Lock()
	ch, closed := s.resize, s.closed
	s.mu.Unlock()
	if closed || ch == nil {
		return
	}
	select {
	case ch <- size:
	default:
		// Sizes are absolute, so dropping one during a drag costs nothing: the
		// next one carries the whole truth.
	}
}

// Close ends the session. Closing the tab must end the shell in the cluster,
// not leave one running with nobody attached.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	w, ch := s.stdin, s.resize
	s.mu.Unlock()

	if w != nil {
		_ = w.Close()
	}
	if ch != nil {
		close(ch)
	}
}

// DefaultCommand is the shell to run when nobody named one.
//
// bash first, sh second, in one invocation: an alpine container has no bash,
// and failing on it would send the developer back to a terminal for the one
// thing they came here to avoid.
//
// The test has to come before the exec, not after it. A failed exec replaces
// the shell with nothing and exits 127; it never reaches an "||" branch, which
// is precisely how the first version of this failed against an alpine image.
var DefaultCommand = []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}

// CommandFor returns what to run.
func CommandFor(requested []string) []string {
	if len(requested) > 0 {
		return requested
	}
	return DefaultCommand
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) {
	// The callback must not keep the buffer: the reader below reuses it.
	b := make([]byte, len(p))
	copy(b, p)
	f(b)
	return len(p), nil
}

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func dialExec(t *testing.T, s *Server, hs *httptest.Server, query string) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/api/exec?" + tokenParam + "=" + s.Token() + "&" + query
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c, c
}

// Keystrokes in, output back, through the local socket and never near an
// apiserver from the browser's side.
func TestExecCarriesKeystrokesAndOutput(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c, _ := dialExec(t, s, hs, "context=qa&namespace=team-a&pod=checkout-1&container=app")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Write(ctx, websocket.MessageBinary, []byte("whoami\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	kind, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != websocket.MessageBinary || !strings.Contains(string(data), "whoami") {
		t.Fatalf("read %v %q, want the echo back", kind, data)
	}
}

// A refusal arrives as a message, so the terminal says why instead of going
// blank.
func TestExecRefusalArrivesAsAMessage(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c, _ := dialExec(t, s, hs, "context=prod&namespace=team-a&pod=checkout-1&container=app")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var m execMessage
	if err := wsjson.Read(ctx, c, &m); err != nil {
		t.Fatalf("read: %v", err)
	}
	if m.Type != "error" || !strings.Contains(m.Message, "pods/exec") {
		t.Fatalf("message = %+v, want the refusal naming the verb", m)
	}
}

// A shell is not something a link should be able to open.
func TestExecNeedsTheToken(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	_, hs := startStream(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/api/exec?context=qa&namespace=team-a&pod=checkout-1"
	c, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("a tokenless exec was accepted")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// The browser's same-origin policy does not cover websockets, and a shell is
// the last thing that should learn that the hard way.
func TestExecRejectsAForeignOrigin(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/api/exec?" + tokenParam + "=" + s.Token() +
		"&context=qa&namespace=team-a&pod=checkout-1"
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: map[string][]string{"Origin": {"https://evil.example"}},
	})
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("a cross-origin exec was accepted")
	}
	if resp == nil || resp.StatusCode != 403 {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// A resize is a text frame; keystrokes are binary. Nothing has to guess which a
// frame is, and a resize must not end up typed into the shell.
func TestResizeIsControlNotInput(t *testing.T) {
	stub := &streamStub{view: view(app("team-a", "checkout", "healthy"))}
	s, hs := startStream(t, stub)
	c, _ := dialExec(t, s, hs, "context=qa&namespace=team-a&pod=checkout-1&container=app")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := wsjson.Write(ctx, c, execMessage{Type: "resize", Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ok\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "resize") {
		t.Fatalf("output = %q; the control message was typed into the shell", data)
	}
}

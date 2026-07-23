package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubAPI struct {
	contexts any
	apps     map[string]any
}

func (s stubAPI) Contexts() any { return s.contexts }

func (s stubAPI) Apps(name string) (any, error) {
	v, ok := s.apps[name]
	if !ok {
		return nil, errors.New("unknown context " + name)
	}
	return v, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(stubAPI{
		contexts: []string{"qa", "prod"},
		apps:     map[string]any{"qa": map[string]any{"apps": 3}},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>kubeside</title>"))
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func do(t *testing.T, s *Server, method, target string, mut func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:7654"
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestTokenIsRequired(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/contexts", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a token", got)
	}
}

func TestValidTokenIsAccepted(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got []string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("contexts = %v", got)
	}
}

func TestBearerTokenIsAccepted(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/contexts", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+s.Token())
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	s := newTestServer(t)
	for _, tok := range []string{"", "wrong", s.Token() + "x", s.Token()[:len(s.Token())-1]} {
		if got := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+tok, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("token %q: status = %d, want 401", tok, got)
		}
	}
}

// A page on another site must not be able to drive this server through the
// user's browser, even with a guessed token.
func TestCrossOriginIsRejectedBeforeTokenCheck(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+s.Token(), func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a foreign Origin", w.Code)
	}
	// Rejecting before the token check means a hostile page learns nothing
	// about whether its guess was right.
	if strings.Contains(w.Body.String(), "token") {
		t.Errorf("response mentions the token: %q", w.Body.String())
	}
}

func TestLoopbackOriginsAreAllowed(t *testing.T) {
	s := newTestServer(t)
	for _, o := range []string{"http://127.0.0.1:7654", "http://localhost:7654", "http://[::1]:7654"} {
		w := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+s.Token(), func(r *http.Request) {
			r.Header.Set("Origin", o)
		})
		if w.Code != http.StatusOK {
			t.Errorf("origin %s: status = %d, want 200", o, w.Code)
		}
	}
}

// DNS rebinding: a hostile name resolves to 127.0.0.1, so the browser treats
// this server as that origin. Checking Host defeats it.
func TestForeignHostIsRejected(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+s.Token(), func(r *http.Request) {
		r.Host = "rebind.evil.example"
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unexpected Host", w.Code)
	}
}

func TestHealthzNeedsNoToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/healthz", nil).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200: healthz must work without a token", got)
	}
}

// Even healthz must not be reachable cross-origin.
func TestHealthzStillChecksOrigin(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/healthz", func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestNoCORSHeadersAreEverSent(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+s.Token(), nil)
	for _, h := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Methods",
	} {
		if v := w.Header().Get(h); v != "" {
			t.Errorf("%s = %q; no other origin may read this server", h, v)
		}
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/contexts?"+tokenParam+"="+s.Token(), nil)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: responses carry cluster data", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q, want a self-only policy", csp)
	}
}

func TestTokensAreUniquePerServer(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s := newTestServer(t)
		if s.Token() == "" {
			t.Fatal("empty token")
		}
		if len(s.Token()) < 40 {
			t.Fatalf("token is only %d chars; too short to resist guessing", len(s.Token()))
		}
		if seen[s.Token()] {
			t.Fatal("token repeated across servers")
		}
		seen[s.Token()] = true
	}
}

func TestAppsRequiresAContext(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/apps?"+tokenParam+"="+s.Token(), nil).Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without a context", got)
	}
}

func TestAppsUnknownContextIs404(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/apps?context=nope&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "nope") {
		t.Errorf("body %q should name the unknown context", w.Body.String())
	}
}

func TestAppsReturnsTheSnapshot(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/apps?context=qa&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["apps"] != float64(3) {
		t.Errorf("apps = %v, want 3", got["apps"])
	}
}

func TestUIIsServedWithAToken(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/?"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want the UI served", w.Code)
	}
	if !strings.Contains(w.Body.String(), "kubeside") {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestListenBindsLoopbackOnly(t *testing.T) {
	l, err := Listen(0)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	addr := l.Addr().String()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("listening on %s; binding anything but loopback exposes cluster credentials to the network", addr)
	}
}

func TestURLCarriesTheToken(t *testing.T) {
	s := newTestServer(t)
	l, err := Listen(0)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	got := s.URL(l)
	if !strings.Contains(got, s.Token()) {
		t.Errorf("URL %q does not carry the token", got)
	}
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("URL %q should be loopback", got)
	}
}

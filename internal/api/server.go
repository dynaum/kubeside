// Package api serves the local UI and the read API behind it.
//
// A local HTTP server holding live cluster credentials is a real attack
// surface, so it is treated as one. Three defences, all enforced here rather
// than assumed:
//
//   - The listener binds 127.0.0.1 only, never 0.0.0.0.
//   - Every request carries a per-session token generated at startup. The
//     browser gets it once, in the URL kubeside opens.
//   - Origin and Host are checked, so a page on another site cannot drive this
//     server through the user's browser.
//
// The browser never talks to an apiserver. It talks to this, and this holds
// the kubeconfig credentials.
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// tokenParam is the query parameter carrying the session token.
const tokenParam = "t"

// Server is the local HTTP server.
type Server struct {
	token   string
	handler http.Handler
	ui      http.Handler
	api     API
}

// API is what the server exposes. Keeping it an interface lets the transport
// be tested without a cluster manager behind it.
type API interface {
	Contexts() any
	Apps(context string) (any, error)
}

// New builds a server with a freshly generated session token.
func New(a API, ui http.Handler) (*Server, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	s := &Server{token: tok, ui: ui, api: a}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/contexts", s.handleContexts)
	mux.HandleFunc("/api/apps", s.handleApps)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if ui != nil {
		mux.Handle("/", ui)
	}

	s.handler = s.withSecurity(mux)
	return s, nil
}

// Token is the session token. Callers put it in the URL they open.
func (s *Server) Token() string { return s.token }

// Handler is the fully wrapped handler, security included. There is no way to
// obtain the bare mux, so the checks cannot be bypassed by accident.
func (s *Server) Handler() http.Handler { return s.handler }

// Listen binds a loopback listener.
//
// The address is hardcoded to 127.0.0.1. Binding 0.0.0.0 would expose cluster
// credentials to the local network, so it is not configurable.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

// URL is the address to open, token included.
func (s *Server) URL(l net.Listener) string {
	return fmt.Sprintf("http://%s/?%s=%s", l.Addr().String(), tokenParam, url.QueryEscape(s.token))
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// withSecurity applies every check. Ordering is deliberate: reject a
// cross-origin request before looking at its token, so a hostile page learns
// nothing about token validity.
func (s *Server) withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Credentials must never be cached by an intermediary or the browser.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The UI is entirely local and loads nothing remote.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		// No CORS headers are ever sent: no other origin may read this.

		if !originAllowed(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if !hostAllowed(r) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/healthz" && !s.tokenValid(r) {
			http.Error(w, "invalid or missing session token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenValid accepts the token from the query string or an Authorization
// header. Comparison is constant time so a timing signal cannot leak it.
func (s *Server) tokenValid(r *http.Request) bool {
	got := r.URL.Query().Get(tokenParam)
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// originAllowed rejects any Origin that is not loopback.
//
// A same-origin request from the UI sends no Origin header on navigation, so
// an absent Origin is allowed; anything present must be loopback.
func originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// hostAllowed defends against DNS rebinding, where a hostile name resolves to
// 127.0.0.1 and the browser then treats the local server as that origin.
func hostAllowed(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func (s *Server) handleContexts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.api.Contexts())
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("context")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context is required"})
		return
	}
	out, err := s.api.Apps(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status is already written, so this can only be logged. Silence
		// would hide a truncated response from the UI.
		_, _ = fmt.Fprintf(w, "\n{\"error\":%q}\n", err.Error())
	}
}

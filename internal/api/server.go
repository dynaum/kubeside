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
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/dynaum/kubeside/internal/exec"
	"github.com/dynaum/kubeside/internal/forward"
	"github.com/dynaum/kubeside/internal/logs"
)

// tokenParam is the query parameter carrying the session token.
const tokenParam = "t"

// Server is the local HTTP server.
type Server struct {
	token   string
	handler http.Handler
	ui      http.Handler
	api     API
	hub     *hub

	// handoff is spent the first time it is presented. It exists so the URL
	// the browser is told to open carries something worthless a second later,
	// rather than the session token itself.
	handoffMu sync.Mutex
	handoff   string
	spent     bool
}

// sessionCookie holds the token once the handoff has been exchanged. HttpOnly,
// so a script on the page cannot read it, and SameSite=Strict, so no other
// origin can cause the browser to send it.
const sessionCookie = "kubeside_session"

// handoffParam is the single-use code in the URL kubeside opens.
const handoffParam = "h"

// API is what the server exposes. Keeping it an interface lets the transport
// be tested without a cluster manager behind it.
//
// The return types are concrete rather than any, because the delta path has to
// diff two views to compute a patch and cannot do that through an interface
// value.
type API interface {
	Contexts() []ContextView
	Apps(context string) (AppsView, error)
	// LogSource opens the log side of one workload. It is separate from Apps
	// because a log stream outlives a single read: the transport keeps it for
	// as long as a window is watching.
	LogSource(contextName, namespace, workload string) (logs.Source, error)
	// Timeline reconstructs one workload's history from the cluster, on demand.
	Timeline(contextName, namespace, workload string) (TimelineView, error)
	// AppDetail is one workload's current state, pods, and history in one read.
	AppDetail(contextName, namespace, workload string) (AppDetailView, error)
	// Config resolves what one container actually received.
	Config(contextName, namespace, workload string) (ConfigView, error)
	// RevealSecret fetches one key of one Secret, gated and recorded.
	RevealSecret(contextName, namespace, secret, key, workload string) (RevealView, error)
	// Diff compares one app's configuration across two environments.
	Diff(req DiffRequest) (DiffView, error)
	// StartForward opens a port-forward, Forwards lists them, StopForward
	// closes one.
	StartForward(req ForwardRequest) (forward.Forward, error)
	Forwards() []forward.Forward
	StopForward(id string) error
	// Promotion compares every app across every environment.
	Promotion() PromotionView
	// Capabilities resolves what this reader may do in one namespace, so every
	// control renders disabled-with-a-reason rather than hidden.
	Capabilities(contextName, namespace string) CapabilitiesView
	// Gate answers what ceremony an action needs, and whether the cluster
	// would permit it at all.
	Gate(req GateRequest) (GateView, error)
	// StartExec opens an interactive shell, after both gates agree.
	StartExec(ctx context.Context, req ExecRequest, onOutput func([]byte)) (*exec.Session, <-chan error, error)
	// Observed reports a row that changed between two reads, which is how the
	// timeline extends forward while kubeside runs.
	Observed(contextName string, before, after AppView)
}

// Option configures a Server.
type Option func(*Server)

// WithPollInterval sets how often a watched view is re-read. Tests use a short
// interval; nothing else needs to set it.
func WithPollInterval(d time.Duration) Option {
	return func(s *Server) { s.hub.interval = d }
}

// New builds a server with a freshly generated session token.
func New(a API, ui http.Handler, opts ...Option) (*Server, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	code, err := newToken()
	if err != nil {
		return nil, err
	}
	s := &Server{token: tok, handoff: code, ui: ui, api: a, hub: newHub(a, DefaultPollInterval)}
	for _, opt := range opts {
		opt(s)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/contexts", s.handleContexts)
	mux.HandleFunc("/api/apps", s.handleApps)
	mux.HandleFunc("/api/app", s.handleAppDetail)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/secret", s.handleReveal)
	mux.HandleFunc("/api/diff", s.handleDiff)
	mux.HandleFunc("/api/forwards", s.handleForwards)
	mux.HandleFunc("/api/promotion", s.handlePromotion)
	mux.HandleFunc("/api/can", s.handleCapabilities)
	mux.HandleFunc("/api/gate", s.handleGate)
	mux.HandleFunc("/api/exec", s.handleExec)
	mux.HandleFunc("/api/timeline", s.handleTimeline)
	mux.HandleFunc("/api/stream", s.handleStream)
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

// Token is the session token. It is never put in a URL.
func (s *Server) Token() string { return s.token }

// Handoff is the single-use code the opened URL carries.
func (s *Server) Handoff() string { return s.handoff }

// redeem exchanges the handoff code for the session token, once.
func (s *Server) redeem(code string) bool {
	s.handoffMu.Lock()
	defer s.handoffMu.Unlock()
	if s.spent || code == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.handoff)) != 1 {
		return false
	}
	s.spent = true
	return true
}

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

// URL is the address to open.
//
// It carries a code that is spent the moment the browser presents it, not the
// session token. The URL becomes an argv another local user can read, and the
// browser writes it to its history; both survive long enough to matter, and
// neither should hold a credential that stays valid.
func (s *Server) URL(l net.Listener) string {
	return fmt.Sprintf("http://%s/?%s=%s", l.Addr().String(), handoffParam, url.QueryEscape(s.handoff))
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
		// The UI loads only its own bundle plus the IBM Plex font files. Fonts
		// are the one documented external request; self-hosting them is a
		// follow-up. Everything else is self.
		//
		// connect-src names the loopback websocket explicitly. 'self' covers
		// it in current browsers, but not in every version that shipped, and a
		// CSP that quietly blocks the socket would leave a screen that renders
		// once and then never moves.
		// This app is never embedded. Framing it is only useful to somebody
		// trying to get a click out of a developer, and both headers are sent
		// because the older one is what some browsers still enforce.
		w.Header().Set("X-Frame-Options", "DENY")
		// The token rides in the URL until it is exchanged, so no request from
		// this page should carry a Referer anywhere.
		w.Header().Set("Referrer-Policy", "no-referrer")
		// No host but this one. IBM Plex is compiled into the binary, so the
		// policy that used to permit Google's font hosts was permitting nothing
		// the product does and something an injected style still could.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; frame-ancestors 'none'; img-src 'self' data:; "+
				// Inline styles only: components set CSS variables and one-off
				// values through the style attribute. Inline *scripts* stay
				// forbidden by default-src, which is why the theme is applied
				// from a module rather than from a script tag in the head.
				"style-src 'self' 'unsafe-inline'; "+
				"font-src 'self'; "+
				// The server binds 127.0.0.1, so the IPv6 loopback form is not needed,
				// and Chromium rejects it as an invalid source anyway.
				"connect-src 'self' ws://127.0.0.1:* ws://localhost:*")
		// No CORS headers are ever sent: no other origin may read this.

		if !originAllowed(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if !hostAllowed(r) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		// Only the API carries cluster data, so only the API requires the
		// token. The static bundle is the same code published in the repo;
		// gating it would break asset loading, since the browser requests
		// /assets/*.js without the token in the URL. Origin and Host checks
		// still apply to every path.
		// The handoff arrives on an ordinary page load. Exchanging it here,
		// before anything else looks at the request, keeps the code out of
		// every handler and guarantees the redirect happens even if the UI is
		// absent.
		if !requiresToken(r.URL.Path) && r.URL.Query().Get(handoffParam) != "" {
			if s.redeem(r.URL.Query().Get(handoffParam)) {
				http.SetCookie(w, &http.Cookie{
					Name:     sessionCookie,
					Value:    s.token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
			}
			// Redirected either way. A code that has been spent must not leave
			// the browser sitting on a URL that looks like it still works.
			clean := *r.URL
			q := clean.Query()
			q.Del(handoffParam)
			clean.RawQuery = q.Encode()
			http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
			return
		}

		if requiresToken(r.URL.Path) && !s.tokenValid(r) {
			// The handoff is spent once, so a tab restored from history after
			// the cookie is gone lands here. Saying which terminal to look at
			// beats a bare 401 nobody can act on.
			http.Error(w, "this browser is not holding a kubeside session; open the URL kubeside printed in your terminal", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requiresToken reports whether a path serves cluster data and must therefore
// carry the session token. Only the API does.
func requiresToken(path string) bool {
	return strings.HasPrefix(path, "/api/")
}

// tokenValid accepts the token from the query string or an Authorization
// header. Comparison is constant time so a timing signal cannot leak it.
func (s *Server) tokenValid(r *http.Request) bool {
	var got string
	if c, err := r.Cookie(sessionCookie); err == nil {
		got = c.Value
	}
	if got == "" {
		got = r.URL.Query().Get(tokenParam)
	}
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

// handleReveal is a POST because it has consequences: it reads a credential and
// writes to the session timeline. A secret value must also never travel in a
// URL, where it would land in history and logs.
func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "reveal must be a POST"})
		return
	}
	var req struct {
		Context   string `json:"context"`
		Namespace string `json:"namespace"`
		Secret    string `json:"secret"`
		Key       string `json:"key"`
		Workload  string `json:"workload"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}
	if req.Context == "" || req.Namespace == "" || req.Secret == "" || req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context, namespace, secret and key are required"})
		return
	}

	out, err := s.api.RevealSecret(req.Context, req.Namespace, req.Secret, req.Key, req.Workload)
	if err != nil {
		var forbidden *ForbiddenError
		if errors.As(err, &forbidden) {
			// The refusal names the verb, so the developer knows what to ask
			// for rather than what went wrong.
			writeJSON(w, http.StatusForbidden, map[string]string{"error": forbidden.Reason})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleForwards lists, opens, and closes port-forwards.
//
// Opening and closing are POSTs because they change what is listening on the
// developer's machine, which is not something a link should be able to do.
// handleGate is a POST because asking can arm an environment: an unlock is a
// consequence, and a link should not be able to cause one.
// handleExec upgrades one tab to a terminal.
//
// The upgrade passes the same Origin, Host, and token checks as every other
// request, and then two more that nothing else needs: the cluster's own answer
// on pods/exec, and the environment's write policy. Only then does a shell
// start.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := ExecRequest{
		Context:   q.Get("context"),
		Namespace: q.Get("namespace"),
		Pod:       q.Get("pod"),
		Container: q.Get("container"),
		Workload:  q.Get("workload"),
		Confirm:   q.Get("confirm"),
	}

	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(execReadLimit)
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	out := make(chan []byte, 64)
	session, done, err := s.api.StartExec(ctx, req, func(b []byte) {
		select {
		case out <- b:
		case <-ctx.Done():
		}
	})
	if err != nil {
		// A refusal arrives as a message rather than a dropped connection, so
		// the terminal can say why instead of going blank.
		_ = wsjson.Write(ctx, c, execMessage{Type: "error", Message: err.Error()})
		_ = c.Close(websocket.StatusPolicyViolation, "exec refused")
		return
	}
	defer session.Close()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-out:
				wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
				err := c.Write(wctx, websocket.MessageBinary, b)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case err := <-done:
				// The shell exited. Saying so beats a terminal that simply
				// stops responding.
				msg := "session ended"
				if err != nil {
					msg = err.Error()
				}
				_ = wsjson.Write(ctx, c, execMessage{Type: "ended", Message: msg})
				_ = c.Close(websocket.StatusNormalClosure, "session ended")
				cancel()
				return
			}
		}
	}()

	for {
		kind, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		switch kind {
		case websocket.MessageBinary:
			session.Write(data)
		case websocket.MessageText:
			// Control messages are text; keystrokes are binary. Nothing has to
			// guess which a frame is.
			var m execMessage
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" {
				session.Resize(exec.Size{Cols: m.Cols, Rows: m.Rows})
			}
		}
	}
}

// execMessage is the control channel: resize going in, errors and endings
// coming back.
type execMessage struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
}

// execReadLimit caps one frame. Keystrokes are bytes; a paste is kilobytes.
const execReadLimit = 1 << 20

func (s *Server) handleGate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "gate must be a POST"})
		return
	}
	var req GateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}
	// A question about an environment names only the environment. A question
	// about an action has to say what it acts on.
	if req.Context == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context is required"})
		return
	}
	if req.Verb != "" && (req.Resource == "" || req.Name == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "an action must name the resource and the object it acts on"})
		return
	}
	out, err := s.api.Gate(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctxName, ns := q.Get("context"), q.Get("namespace")
	if ctxName == "" || ns == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context and namespace are required"})
		return
	}
	writeJSON(w, http.StatusOK, s.api.Capabilities(ctxName, ns))
}

// handlePromotion answers "is the fix in prod yet".
func (s *Server) handlePromotion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.api.Promotion())
}

func (s *Server) handleForwards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.api.Forwards())

	case http.MethodPost:
		var req struct {
			ForwardRequest
			Stop string `json:"stop"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
			return
		}
		if req.Stop != "" {
			if err := s.api.StopForward(req.Stop); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, s.api.Forwards())
			return
		}
		if req.Context == "" || req.Namespace == "" || req.Workload == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context, namespace and workload are required"})
			return
		}
		f, err := s.api.StartForward(req.ForwardRequest)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, f)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET to list or POST to open and close"})
	}
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := DiffRequest{
		Context:        q.Get("context"),
		Namespace:      q.Get("namespace"),
		Workload:       q.Get("workload"),
		Other:          q.Get("other"),
		OtherNamespace: q.Get("otherNamespace"),
		OtherWorkload:  q.Get("otherWorkload"),
		Container:      q.Get("container"),
	}
	if req.Context == "" || req.Namespace == "" || req.Workload == "" || req.Other == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context, namespace, workload and other are required"})
		return
	}
	if req.Other == req.Context && req.OtherNamespace == "" && req.OtherWorkload == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "comparing a context with itself says nothing"})
		return
	}
	out, err := s.api.Diff(req)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctxName, ns, workload := q.Get("context"), q.Get("namespace"), q.Get("workload")
	if ctxName == "" || ns == "" || workload == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context, namespace and workload are required"})
		return
	}
	out, err := s.api.Config(ctxName, ns, workload)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctxName, ns, workload := q.Get("context"), q.Get("namespace"), q.Get("workload")
	if ctxName == "" || ns == "" || workload == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context, namespace and workload are required"})
		return
	}
	out, err := s.api.AppDetail(ctxName, ns, workload)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTimeline reconstructs history on demand, which is when a developer
// opens an app rather than at startup: launch stays cheap.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctxName, ns, workload := q.Get("context"), q.Get("namespace"), q.Get("workload")
	if ctxName == "" || ns == "" || workload == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context, namespace and workload are required"})
		return
	}
	out, err := s.api.Timeline(ctxName, ns, workload)
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

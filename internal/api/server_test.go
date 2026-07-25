package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"time"

	"github.com/dynaum/kubeside/internal/forward"
	"github.com/dynaum/kubeside/internal/logs"
	"github.com/dynaum/kubeside/internal/promotion"
	"github.com/dynaum/kubeside/internal/rbac"
	"github.com/dynaum/kubeside/internal/resolved"
	"github.com/dynaum/kubeside/internal/timeline"
)

type stubAPI struct {
	contexts []ContextView
	apps     map[string]AppsView
}

func (s stubAPI) Contexts() []ContextView { return s.contexts }

func (s stubAPI) LogSource(_, _, _ string) (logs.Source, error) {
	return nil, errors.New("no log source in this test")
}

func (s stubAPI) Observed(string, AppView, AppView) {}

func (s stubAPI) Capabilities(contextName, namespace string) CapabilitiesView {
	allowed := contextName == "qa"
	can := map[string]rbac.Permission{}
	for _, a := range []rbac.Action{{Verb: "create", Resource: "pods", Subresource: "exec", Namespace: namespace}} {
		if allowed {
			can[a.Key()] = rbac.Permission{Allowed: true}
			continue
		}
		can[a.Key()] = rbac.Permission{Reason: "needs " + a.Key()}
	}
	return CapabilitiesView{Context: contextName, Namespace: namespace, Can: can}
}

func (s stubAPI) Promotion() PromotionView {
	envs := []promotion.Env{{Name: "qa"}, {Name: "prod"}}
	rows := promotion.Build(envs, []promotion.Instance{
		{Env: "qa", App: "checkout", Namespace: "team-a", Present: true, Tag: "v2"},
		{Env: "prod", App: "checkout", Namespace: "team-a", Present: true, Tag: "v1"},
	})
	return PromotionView{Envs: envs, Rows: rows, Summary: promotion.Summarize(rows)}
}

func (s stubAPI) StartForward(req ForwardRequest) (forward.Forward, error) {
	if req.RemotePort == 0 {
		return forward.Forward{}, errors.New("a container port is required")
	}
	return forward.Forward{
		ID: "fwd-1", Context: req.Context, Namespace: req.Namespace, Workload: req.Workload,
		RemotePort: req.RemotePort, LocalPort: 51234, Address: "127.0.0.1:51234",
		State: forward.StateReady, Environment: "qa",
	}, nil
}

func (s stubAPI) Forwards() []forward.Forward {
	return []forward.Forward{{ID: "fwd-1", Workload: "checkout", LocalPort: 51234, State: forward.StateReady}}
}

func (s stubAPI) StopForward(id string) error {
	if id != "fwd-1" {
		return errors.New("no forward " + id)
	}
	return nil
}

func (s stubAPI) Diff(req DiffRequest) (DiffView, error) {
	if req.Other == "unreachable" {
		return DiffView{}, errors.New("unreachable is not connected")
	}
	return DiffView{
		Left:      DiffSide{Context: req.Context, Namespace: req.Namespace, Workload: req.Workload},
		Right:     DiffSide{Context: req.Other, Namespace: req.Namespace, Workload: req.Workload},
		Container: "app",
		Rows: []resolved.CrossRow{
			{Key: "LOG_LEVEL", Left: "debug", Right: "debug", Class: resolved.ClassSuspicious},
			{Key: "RETRY_LIMIT", Left: "3", RightUnset: true, Class: resolved.ClassMissing},
		},
		Summary: resolved.Summary{Suspicious: 1, Missing: 1},
	}, nil
}

func (s stubAPI) RevealSecret(_, _, secret, key, _ string) (RevealView, error) {
	if secret == "locked" {
		return RevealView{}, &ForbiddenError{Reason: "needs get on secret locked in team-a"}
	}
	if key != "STRIPE_SECRET_KEY" {
		return RevealView{}, errors.New("no key " + key)
	}
	return RevealView{Secret: secret, Key: key, Value: "sk_live_donotleak"}, nil
}

func (s stubAPI) Config(contextName, namespace, workload string) (ConfigView, error) {
	if workload != "checkout" {
		return ConfigView{}, errors.New("no workload " + workload)
	}
	return ConfigView{
		Context: contextName, Namespace: namespace, Workload: workload, Pod: "checkout-1",
		Caveat: "values are read from the ConfigMaps as they are now",
		Containers: []resolved.Container{{
			Name: "app",
			Values: []resolved.Value{
				{Key: "LOG_LEVEL", Value: "debug", Source: resolved.Source{Kind: resolved.SourceInline}},
				{Key: "PASSWORD", Masked: true, Source: resolved.Source{Kind: resolved.SourceSecret, Ref: "db", Key: "PASSWORD"}},
			},
		}},
	}, nil
}

func (s stubAPI) AppDetail(contextName, namespace, workload string) (AppDetailView, error) {
	tl, err := s.Timeline(contextName, namespace, workload)
	if err != nil {
		return AppDetailView{}, err
	}
	return AppDetailView{
		Context: contextName, Namespace: namespace, Workload: workload, Kind: "Deployment",
		Health: "degraded", Ready: "5/6", Image: "checkout:1.2.0", Restarts: 14,
		Pods:     []PodView{{Name: "checkout-1", Health: "failed", Restarts: 14}},
		Timeline: tl,
	}, nil
}

func (s stubAPI) Timeline(contextName, namespace, workload string) (TimelineView, error) {
	if workload != "checkout" {
		return TimelineView{}, errors.New("no workload " + workload)
	}
	return TimelineView{
		Context: contextName, Namespace: namespace, Workload: workload,
		Entries: []timeline.Entry{{At: time.Unix(0, 0), Kind: timeline.KindDeploy, Title: "revision 3"}},
		Gaps:    []timeline.Gap{{Source: "helm", Reason: "needs read access to secrets in this namespace"}},
	}, nil
}

func (s stubAPI) Apps(name string) (AppsView, error) {
	v, ok := s.apps[name]
	if !ok {
		return AppsView{}, errors.New("unknown context " + name)
	}
	return v, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(stubAPI{
		contexts: []ContextView{{Name: "qa", Current: true}, {Name: "prod"}},
		apps: map[string]AppsView{"qa": view(
			app("team-a", "checkout", "healthy"),
			app("team-b", "search", "failed"),
			app("team-b", "billing", "healthy"),
		)},
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
	var got []ContextView
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
	var got AppsView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Apps) != 3 {
		t.Errorf("apps = %d, want 3", len(got.Apps))
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

// The static bundle is public code and must load without the token, or the
// browser's tokenless request for /assets/*.js 401s and nothing renders.
func TestStaticServedWithoutToken(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("static / without token = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "kubeside") {
		t.Errorf("body = %q", w.Body.String())
	}
}

// But the API, which carries cluster data, still requires the token.
func TestAPIStillRequiresTokenAfterStaticExemption(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/contexts", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("api without token = %d, want 401", got)
	}
	if got := do(t, s, "GET", "/api/apps?context=qa", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("apps without token = %d, want 401", got)
	}
}

func TestCSPAllowsPlexFontsOnly(t *testing.T) {
	s := newTestServer(t)
	csp := do(t, s, "GET", "/", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "fonts.googleapis.com") || !strings.Contains(csp, "fonts.gstatic.com") {
		t.Errorf("CSP should permit the Plex font host: %q", csp)
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow eval: %q", csp)
	}
}

func TestTimelineRequiresEveryCoordinate(t *testing.T) {
	s := newTestServer(t)
	for _, target := range []string{
		"/api/timeline?",
		"/api/timeline?context=qa&",
		"/api/timeline?context=qa&namespace=team-a&",
	} {
		if got := do(t, s, "GET", target+tokenParam+"="+s.Token(), nil).Code; got != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", target, got)
		}
	}
}

func TestTimelineReturnsEntriesAndGaps(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/timeline?context=qa&namespace=team-a&workload=checkout&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got TimelineView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Kind != timeline.KindDeploy {
		t.Errorf("entries = %+v", got.Entries)
	}
	// A source that could not be read travels with the answer, never silently.
	if len(got.Gaps) != 1 || got.Gaps[0].Source != "helm" {
		t.Errorf("gaps = %+v", got.Gaps)
	}
}

func TestTimelineUnknownWorkloadIs404(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/timeline?context=qa&namespace=team-a&workload=ghost&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestTimelineNeedsTheToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/timeline?context=qa&namespace=team-a&workload=checkout", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: the timeline carries cluster data", got)
	}
}

func TestAppDetailReturnsStatePodsAndHistory(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/app?context=qa&namespace=team-a&workload=checkout&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got AppDetailView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ready != "5/6" || len(got.Pods) != 1 {
		t.Errorf("view = %+v, want state and pods in one read", got)
	}
	if len(got.Timeline.Entries) == 0 {
		t.Error("the detail view arrived without its history")
	}
}

func TestAppDetailRequiresEveryCoordinate(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/app?context=qa&"+tokenParam+"="+s.Token(), nil).Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

func TestAppDetailNeedsTheToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/app?context=qa&namespace=team-a&workload=checkout", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

func TestConfigReturnsResolvedValues(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/config?context=qa&namespace=team-a&workload=checkout&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got ConfigView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Containers) != 1 || len(got.Containers[0].Values) != 2 {
		t.Fatalf("view = %+v", got)
	}
	if got.Caveat == "" {
		t.Error("the reading arrived without its caveat")
	}
}

// The wire must not carry a secret value even by accident.
func TestConfigNeverSerializesASecretValue(t *testing.T) {
	s := newTestServer(t)
	body := do(t, s, "GET", "/api/config?context=qa&namespace=team-a&workload=checkout&"+tokenParam+"="+s.Token(), nil).Body.String()
	if strings.Contains(body, "hunter2") || strings.Contains(body, "\"value\":\"\\u003c") {
		t.Fatalf("body carries something that looks like a secret: %s", body)
	}
	if !strings.Contains(body, "\"masked\":true") {
		t.Errorf("body does not mark the masked value: %s", body)
	}
}

func TestConfigNeedsTheToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/config?context=qa&namespace=team-a&workload=checkout", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

func postJSON(t *testing.T, s *Server, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.Host = "127.0.0.1:7654"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestRevealReturnsTheValueToAPermittedReader(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/secret?"+tokenParam+"="+s.Token(),
		`{"context":"qa","namespace":"team-a","secret":"payments-stripe","key":"STRIPE_SECRET_KEY","workload":"checkout"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got RevealView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "sk_live_donotleak" {
		t.Errorf("value = %q", got.Value)
	}
}

// A refusal is a 403 that names the verb, not a generic failure.
func TestRevealRefusalIs403AndNamesTheVerb(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/secret?"+tokenParam+"="+s.Token(),
		`{"context":"qa","namespace":"team-a","secret":"locked","key":"K"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "get on secret locked") {
		t.Errorf("body = %q, should name what is needed", w.Body.String())
	}
}

// A secret value must never travel in a URL, where it lands in history and
// logs. The endpoint refuses anything but POST.
func TestRevealRefusesGET(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/secret?"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestRevealNeedsTheToken(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/secret",
		`{"context":"qa","namespace":"team-a","secret":"payments-stripe","key":"STRIPE_SECRET_KEY"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRevealRequiresEveryCoordinate(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/secret?"+tokenParam+"="+s.Token(), `{"context":"qa","namespace":"team-a"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDiffComparesTwoEnvironments(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/diff?context=qa&namespace=team-a&workload=checkout&other=prod&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got DiffView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Rows) != 2 || got.Summary.Missing != 1 {
		t.Fatalf("view = %+v", got)
	}
}

// Comparing a context with itself produces a table of matches and answers
// nothing, so it is refused rather than rendered.
func TestDiffRefusesToCompareAContextWithItself(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/diff?context=qa&namespace=team-a&workload=checkout&other=qa&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestDiffRequiresBothSides(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/diff?context=qa&namespace=team-a&workload=checkout&"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without the other side", w.Code)
	}
}

func TestDiffNeedsTheToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/diff?context=qa&namespace=team-a&workload=checkout&other=prod", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

func TestForwardsListLiveTunnels(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/forwards?"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0]["localPort"] != float64(51234) {
		t.Fatalf("forwards = %+v", got)
	}
}

func TestForwardOpensATunnel(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/forwards?"+tokenParam+"="+s.Token(),
		`{"context":"qa","namespace":"team-a","workload":"checkout","remotePort":8080}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "127.0.0.1:51234") {
		t.Errorf("body = %q, should carry the address to hit", w.Body.String())
	}
}

func TestForwardWithoutAPortIsRefused(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/forwards?"+tokenParam+"="+s.Token(),
		`{"context":"qa","namespace":"team-a","workload":"checkout"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestStoppingAForwardReturnsWhatIsLeft(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/forwards?"+tokenParam+"="+s.Token(), `{"stop":"fwd-1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}

func TestStoppingAnUnknownForwardIs404(t *testing.T) {
	s := newTestServer(t)
	w := postJSON(t, s, "/api/forwards?"+tokenParam+"="+s.Token(), `{"stop":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// A tunnel into the cluster is not something a link should be able to open.
func TestForwardsNeedTheToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/forwards", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

func TestPromotionReturnsTheMatrix(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "GET", "/api/promotion?"+tokenParam+"="+s.Token(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got PromotionView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Envs) != 2 || len(got.Rows) != 1 {
		t.Fatalf("view = %+v", got)
	}
	if got.Summary.Drifted != 1 {
		t.Errorf("summary = %+v, want the drifted app counted", got.Summary)
	}
}

func TestPromotionNeedsTheToken(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/promotion", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
}

// The same control on the same screen is enabled in qa and disabled in prod,
// in one session. That is the whole point of resolving per context.
func TestCapabilitiesResolvePerContext(t *testing.T) {
	s := newTestServer(t)

	var qa CapabilitiesView
	w := do(t, s, "GET", "/api/can?context=qa&namespace=team-a&"+tokenParam+"="+s.Token(), nil)
	if err := json.NewDecoder(w.Body).Decode(&qa); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !qa.Can["create pods/exec in team-a"].Allowed {
		t.Fatalf("qa = %+v, want exec allowed", qa.Can)
	}

	var prod CapabilitiesView
	w = do(t, s, "GET", "/api/can?context=prod&namespace=team-a&"+tokenParam+"="+s.Token(), nil)
	if err := json.NewDecoder(w.Body).Decode(&prod); err != nil {
		t.Fatalf("decode: %v", err)
	}
	p := prod.Can["create pods/exec in team-a"]
	if p.Allowed {
		t.Fatal("prod must not inherit qa's permission")
	}
	if !strings.Contains(p.Reason, "create pods/exec") {
		t.Errorf("reason = %q, should name the verb the control needs", p.Reason)
	}
}

func TestCapabilitiesRequireBothCoordinates(t *testing.T) {
	s := newTestServer(t)
	if got := do(t, s, "GET", "/api/can?context=qa&"+tokenParam+"="+s.Token(), nil).Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

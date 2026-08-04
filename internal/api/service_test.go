package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/promotion"
	"github.com/dynaum/kubeside/internal/session"
	"github.com/dynaum/kubeside/internal/timeline"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// fakeApp is one workload to seed into a fake cluster: a Deployment running
// one image, which is all the promotion matrix needs to compare versions.
type fakeApp struct {
	name, ns, image string
	// imageID seeds a running pod owned by the Deployment, reporting this as
	// the image it actually resolved to. The digest lives in pod status and
	// nowhere else, so a fixture without one can only ever produce a placement
	// whose digest is still pending.
	imageID string
}

// fakeCluster is one kubeconfig context's worth of fixture: which environment
// it binds to, what it runs, and whether it can be reached or read at all.
type fakeCluster struct {
	env  string
	apps []fakeApp
	// unreachable fails the connection itself, the way an expired token, a
	// refused credential, or an off-VPN cluster does. clusters.Fetch has no
	// path that returns a hard error, so every connect-time failure takes
	// this one branch in Service.Promotion() and Service.Fleet() regardless
	// of which of those causes produced it; the distinction between them is
	// not one either method can observe.
	unreachable bool
	// refusedKinds fails List for exactly these kinds, once connected, the
	// way a namespace-scoped role refuses a cluster-scoped read. Unlike
	// unreachable, clusters.Fetch has no error path for this: the refused
	// kinds land in Snapshot.Partial and Fetch returns a nil error, which is
	// what lets a caller tell "denied" apart from "absent".
	refusedKinds []string
	// slow delays the connection by this much, the way a cluster behind a VPN
	// that is off does. It is what separates a sweep that spends one budget
	// serially from one that gives every cluster its own.
	slow time.Duration
}

// serviceWithContexts builds a Service over several kubeconfig contexts, each
// bound to an environment and backed by its own fake clientset. It is the
// multi-cluster sibling of serviceWith: where that helper proves one context
// works, this one proves several contexts bound to the same environment are
// all read, not just the first.
func serviceWithContexts(t *testing.T, byContext map[string]fakeCluster) *Service {
	t.Helper()

	names := make([]string, 0, len(byContext))
	for name := range byContext {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("serviceWithContexts needs at least one context")
	}

	var envOrder []string
	envContexts := map[string][]string{}
	contexts := make([]kubeconfig.Context, 0, len(names))
	for _, name := range names {
		fc := byContext[name]
		if _, ok := envContexts[fc.env]; !ok {
			envOrder = append(envOrder, fc.env)
		}
		envContexts[fc.env] = append(envContexts[fc.env], name)
		contexts = append(contexts, kubeconfig.Context{
			Name: name, IsCurrent: name == names[0], Server: "https://" + name,
		})
	}

	var b strings.Builder
	b.WriteString("environments:\n")
	for _, env := range envOrder {
		b.WriteString("  - name: " + env + "\n")
		b.WriteString("    contexts: [" + strings.Join(envContexts[env], ", ") + "]\n")
	}

	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	conf, err := config.Load(config.Options{Path: p})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	connector := clusters.KubeConnector{
		NewClient: func(kctx kubeconfig.Context, _ kubeconfig.Options) (kubernetes.Interface, error) {
			fc := byContext[kctx.Name]
			if fc.slow > 0 {
				time.Sleep(fc.slow)
			}
			if fc.unreachable {
				return nil, fmt.Errorf("dial %s: connection refused", kctx.Name)
			}
			return fakeClientFor(fc.apps, fc.refusedKinds), nil
		},
	}

	kcfg := &kubeconfig.Config{Current: names[0], Contexts: contexts}
	mgr := clusters.New(kcfg, connector, clusters.Options{})
	t.Cleanup(mgr.Close)
	return NewService(kcfg, mgr, kubeconfig.Options{}, conf, time.Second)
}

// fakeClientFor builds a fake clientset with one Deployment per app, mirroring
// clusterWithApp: a selector matching its own pod template is enough for the
// grouping engine to recognize it, and no pod is needed for a version compare.
//
// refusedKinds makes List fail for exactly those kinds with the same
// apierrors.NewForbidden a namespace-scoped role produces, so a caller can
// build the "connected, but this kind is unreadable" fixture that a plain
// unreachable context cannot: clusters.Fetch turns this into an entry in
// Snapshot.Partial rather than an error.
func fakeClientFor(apps []fakeApp, refusedKinds []string) *fake.Clientset {
	objs := make([]runtime.Object, 0, len(apps))
	for _, a := range apps {
		objs = append(objs, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: a.name, Namespace: a.ns, UID: types.UID(a.ns + "/" + a.name)},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": a.name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": a.name}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: a.image}}},
				},
			},
		})
	}
	for _, a := range apps {
		if a.imageID == "" {
			continue
		}
		// The pod is owned by its Deployment, so the grouping engine walks up
		// to the same app rather than inventing a second one, and its container
		// status carries the digest the node actually pulled.
		yes := true
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: a.name + "-abc123", Namespace: a.ns, UID: types.UID(a.ns + "/" + a.name + "/pod"),
				Labels: map[string]string{"app": a.name},
				OwnerReferences: []metav1.OwnerReference{{
					Kind: "Deployment", Name: a.name,
					UID: types.UID(a.ns + "/" + a.name), Controller: &yes,
				}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: a.image}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app", Image: a.image, ImageID: a.imageID, Ready: true,
				}},
			},
		})
	}
	client := fake.NewSimpleClientset(objs...)
	for _, kind := range refusedKinds {
		resource := resourceOfKind(kind)
		client.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", nil)
		})
	}
	return client
}

// resourceOfKind maps the kind names clusters.Fetch reports in Snapshot.Partial
// to the plural resource name a fake clientset reactor matches on.
func resourceOfKind(kind string) string {
	switch kind {
	case "Deployment":
		return "deployments"
	case "StatefulSet":
		return "statefulsets"
	case "DaemonSet":
		return "daemonsets"
	case "CronJob":
		return "cronjobs"
	case "Job":
		return "jobs"
	case "ReplicaSet":
		return "replicasets"
	case "Pod":
		return "pods"
	}
	return strings.ToLower(kind) + "s"
}

// Two contexts bound to one environment, running different versions. Build
// the service with both contexts pointing at prod, the second serving
// checkout at v2.12.0 and the first at v2.13.1.
func TestPromotionKeepsEveryContextInAnEnvironment(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-us-east": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.13.1"}}},
		"prod-eu-west": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.12.0"}}},
	})

	v := s.Promotion()

	if len(v.Envs) != 1 {
		t.Fatalf("envs = %d, want 1: both contexts bind to prod", len(v.Envs))
	}
	if got := len(v.Envs[0].Contexts); got != 2 {
		t.Errorf("prod contexts = %d, want 2: the second context was dropped", got)
	}
	if len(v.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(v.Rows))
	}
	c := v.Rows[0].Cells[0]
	if c.State != promotion.StateSplit {
		t.Errorf("state = %q, want %q: two clusters on different versions is not one version", c.State, promotion.StateSplit)
	}
}

func TestPromotionMarksAnUnreachableContextOnItsEnvironment(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-us-east": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.13.1"}}},
		"prod-eu-west": {env: "prod", unreachable: true},
	})

	v := s.Promotion()

	if len(v.Envs) != 1 {
		t.Fatalf("envs = %d, want 1", len(v.Envs))
	}
	if got := v.Envs[0].Unreadable; len(got) != 1 || got[0] != "prod-eu-west" {
		t.Errorf("unreadable = %v, want [prod-eu-west]", got)
	}
	if v.Rows[0].Cells[0].State != promotion.StateSplit {
		t.Error("a cluster nobody could read is not an agreement")
	}
}

// A partially-unreachable environment (one context down, one reads fine) must
// not appear in PromotionView.Unreachable: the split cell and Env.Unreadable
// already say a cluster there could not be read, and the banner claiming the
// whole column is unknown while it renders real split data would contradict
// what is on screen. A fully-unreachable environment, where every bound
// context failed, still belongs in the banner.
func TestPromotionUnreachableOnlyForFullyUnreadableEnvironments(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-us-east": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.13.1"}}},
		"prod-eu-west": {env: "prod", unreachable: true},
		"stg-a":        {env: "stg", unreachable: true},
		"stg-b":        {env: "stg", unreachable: true},
	})

	v := s.Promotion()

	for _, name := range v.Unreachable {
		if name == "prod" {
			t.Errorf("Unreachable = %v, must not include prod: one of its two contexts reads fine", v.Unreachable)
		}
	}
	found := false
	for _, name := range v.Unreachable {
		if name == "stg" {
			found = true
		}
	}
	if !found {
		t.Errorf("Unreachable = %v, want stg: every context bound to it failed", v.Unreachable)
	}
}

// dedupeStrings is what keeps an environment with several unreachable
// contexts down to one banner entry. Exercise it directly, since nothing
// about the code path it serves proves the collapsing behavior on its own.
func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"prod", "stg", "prod", "prod", "qa"})
	want := []string{"prod", "stg", "qa"}
	if len(got) != len(want) {
		t.Fatalf("dedupeStrings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeStrings = %v, want %v", got, want)
		}
	}
}

func serviceFor(t *testing.T, body string, contexts ...kubeconfig.Context) *Service {
	t.Helper()

	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	conf, err := config.Load(config.Options{Path: p})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	kcfg := &kubeconfig.Config{Current: contexts[0].Name, Contexts: contexts}
	mgr := clusters.New(kcfg, nil, clusters.Options{})
	return NewService(kcfg, mgr, kubeconfig.Options{}, conf, time.Second)
}

// The rail colours and the hazard hatch come from the backend, so the browser
// never has to re-guess what an environment is.
func TestContextViewCarriesTheResolvedEnvironment(t *testing.T) {
	svc := serviceFor(t, `
environments:
  - name: qa
    risk: low
    color: green
    write: allow
    clusters: [https://api.example.com]
`, kubeconfig.Context{Name: "prod-us-east", IsCurrent: true, Server: "https://api.example.com"})

	views := svc.Contexts()
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	v := views[0]
	// The context is named prod; the config binds its cluster to qa. Config wins.
	if v.Environment != "qa" || v.Risk != "low" || v.Write != "allow" || v.Color != "green" {
		t.Fatalf("view = %+v, want the configured qa environment", v)
	}
	if v.Hazard {
		t.Error("a low-risk environment must not render as hazardous")
	}
}

func TestContextViewFallsBackToNameClassification(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "prod-us-east", IsCurrent: true, Server: "https://api"})

	v := svc.Contexts()[0]
	if v.Environment != "prod-us-east" || v.Risk != "high" || v.Write != "deny" {
		t.Fatalf("view = %+v, want prod guardrails inferred from the name", v)
	}
	if !v.Hazard {
		t.Error("a high-risk environment must carry the hazard marking")
	}
}

// An unrecognized name is never rendered as safe.
func TestUnclassifiedContextIsHazardous(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "atlantis", IsCurrent: true, Server: "https://api"})

	v := svc.Contexts()[0]
	if v.Risk != "high" || !v.Hazard || v.Write != "deny" {
		t.Fatalf("view = %+v, want an unclassified context treated as dangerous", v)
	}
}

// A config with no metrics preference leaves the probe to decide; an explicit
// none must switch the usage columns off rather than render zeroes.
func TestConfiguredMetricsSourceIsHonored(t *testing.T) {
	svc := serviceFor(t, "defaults:\n  metrics: none\n", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	info := svc.metricsInfo(t.Context(), "qa1")
	if info.Available {
		t.Fatalf("metrics = %+v, want unavailable when the config says none", info)
	}
	if info.Source != "none" {
		t.Errorf("source = %q, want none", info.Source)
	}
}

// The live half of the timeline: what kubeside watched happen while it ran.
func TestObservedRecordsAHealthTransition(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	before := AppView{Namespace: "team-a", Name: "checkout", Health: "healthy"}
	after := AppView{Namespace: "team-a", Name: "checkout", Health: "failed", Detail: "pod checkout-1 is in CrashLoopBackOff"}
	svc.Observed("qa1", before, after)

	got := svc.live.Entries(sessionKey("qa1", "team-a", "checkout"))
	if len(got) != 1 {
		t.Fatalf("entries = %+v, want the transition", got)
	}
	if got[0].Title != "healthy → failed" || got[0].Kind != timeline.KindHealth {
		t.Fatalf("entry = %+v", got[0])
	}
	if got[0].Detail != after.Detail {
		t.Errorf("detail = %q, should carry why", got[0].Detail)
	}
}

// Replica counts churn through every rollout. A timeline of them buries the one
// line that matters.
func TestObservedIgnoresAChangeThatIsNotHealth(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	before := AppView{Namespace: "team-a", Name: "checkout", Health: "healthy", Ready: "3/4"}
	after := AppView{Namespace: "team-a", Name: "checkout", Health: "healthy", Ready: "4/4"}
	svc.Observed("qa1", before, after)

	if got := svc.live.Entries(sessionKey("qa1", "team-a", "checkout")); len(got) != 0 {
		t.Fatalf("entries = %+v, want none for a replica change", got)
	}
}

// A first sighting has nothing to compare against, and "it appeared" is the
// app list's job, not the timeline's.
func TestObservedIgnoresAnAppSeenForTheFirstTime(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	svc.Observed("qa1", AppView{}, AppView{Namespace: "team-a", Name: "new", Health: "progressing"})

	if got := svc.live.Entries(sessionKey("qa1", "team-a", "new")); len(got) != 0 {
		t.Fatalf("entries = %+v, want none for a first sighting", got)
	}
}

// The session marker is mandatory, per docs/03-product-spec.md. An app that
// has been watched all along without changing still gets one: "we were
// watching and nothing happened" is a different answer from "we have no idea",
// and an unmarked lane says the wrong one.
func TestTimelineAlwaysCarriesTheSessionMarker(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	h := svc.sessionHorizon(sessionKey("qa1", "team-a", "never-changed"))
	if h.Source != "session" || h.Pruned {
		t.Fatalf("horizon = %+v, want an unpruned session marker", h)
	}
	if !strings.Contains(h.Reason, "kubeside started") {
		t.Errorf("reason = %q", h.Reason)
	}
	if h.At.Before(svc.startedAt) || h.At.After(time.Now()) {
		t.Errorf("marker at %v, want the moment this process began watching", h.At)
	}
}

// Once the buffer has evicted, the marker changes meaning: a cut in something
// that was known, not the beginning of watching.
func TestEvictedSessionMarkerReplacesTheStartMarker(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})
	svc.live = session.New(session.Limits{MaxEntriesPerApp: 1})

	key := sessionKey("qa1", "team-a", "checkout")
	for i := 0; i < 3; i++ {
		svc.live.Record(key, timeline.Entry{
			At: time.Now().Add(time.Duration(i) * time.Second), Kind: timeline.KindHealth,
			Title: "healthy → failed", Source: "session",
		})
	}

	h := svc.sessionHorizon(key)
	if !h.Pruned || !strings.Contains(h.Reason, "evicted") {
		t.Fatalf("horizon = %+v, want the eviction marker", h)
	}
}

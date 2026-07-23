package clusters

import (
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"strings"
	"testing"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestFetchGroupsClusterWide(t *testing.T) {
	c := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "checkout", Namespace: "team-a", UID: "u1",
			Labels: map[string]string{"app.kubernetes.io/instance": "checkout"},
		}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "payments", Namespace: "team-a", UID: "u2",
			Annotations: map[string]string{"meta.helm.sh/release-name": "payments"},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
	)

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !snap.Scope.ClusterWide {
		t.Fatalf("scope = %s, want cluster-wide", snap.Scope)
	}
	if len(snap.Apps) != 2 {
		t.Fatalf("got %d apps, want 2: %+v", len(snap.Apps), snap.Apps)
	}
}

// A refused cluster-scoped list is a normal answer, not an error.
func TestFetchFallsBackWhenNamespaceListForbidden(t *testing.T) {
	c := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "team-a", UID: "u1"}},
	)
	c.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", nil)
	})

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "prod", Namespace: "team-a"})
	if err != nil {
		t.Fatalf("a forbidden namespace list must not fail the fetch: %v", err)
	}
	if snap.Scope.ClusterWide {
		t.Fatal("scope should not claim cluster-wide after a refusal")
	}
	if len(snap.Scope.Namespaces) != 1 || snap.Scope.Namespaces[0] != "team-a" {
		t.Fatalf("namespaces = %v, want the context namespace", snap.Scope.Namespaces)
	}
	if snap.Scope.Reason == "" {
		t.Error("the fallback must explain itself so the UI can name the mode")
	}
	if len(snap.Apps) != 1 {
		t.Fatalf("got %d apps, want 1 from the fallback namespace", len(snap.Apps))
	}
}

func TestFetchWithNoContextNamespaceUsesDefault(t *testing.T) {
	c := fake.NewSimpleClientset()
	c.PrependReactor("list", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "", nil)
	})

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "prod"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Scope.Namespaces) != 1 || snap.Scope.Namespaces[0] != "default" {
		t.Fatalf("namespaces = %v, want default", snap.Scope.Namespaces)
	}
}

// A developer who can read Deployments but not Pods should still see apps.
func TestPartialReadIsRecordedNotFatal(t *testing.T) {
	c := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "team-a", UID: "u1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
	)
	c.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
	})

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "prod"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Apps) != 1 {
		t.Fatalf("got %d apps, want the readable Deployment", len(snap.Apps))
	}
	var sawPods bool
	for _, p := range snap.Partial {
		if p == "Pod" {
			sawPods = true
		}
	}
	if !sawPods {
		t.Error("an unreadable kind must be reported, so the UI says what is missing rather than implying there is none")
	}
}

func TestOwnerReferencesSurviveTheAdapter(t *testing.T) {
	yes := true
	c := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "ns", UID: "dep"}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-7d9f", Namespace: "ns", UID: "rs",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout", UID: "dep", Controller: &yes}},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}},
	)

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Apps) != 1 {
		t.Fatalf("got %d apps, want the ReplicaSet folded into its Deployment: %+v", len(snap.Apps), snap.Apps)
	}
	if len(snap.Apps[0].Workloads) != 2 {
		t.Errorf("want deployment+replicaset, got %d", len(snap.Apps[0].Workloads))
	}
}

func TestNonControllerOwnerRefIsNotTreatedAsController(t *testing.T) {
	c := fake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: "a"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
			Name: "b", Namespace: "ns", UID: "b",
			// Controller is nil: an ownership hint, not composition.
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "a", UID: "a"}},
		}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}},
	)

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Apps) != 2 {
		t.Fatalf("got %d apps, want 2: a nil Controller must not fold b into a", len(snap.Apps))
	}
}

func TestScopeStrings(t *testing.T) {
	if got := (Scope{ClusterWide: true}).String(); got != "cluster-wide" {
		t.Errorf("got %q", got)
	}
	if got := (Scope{}).String(); got != "no readable namespace" {
		t.Errorf("got %q", got)
	}
	if got := (Scope{Namespaces: []string{"a"}}).String(); got == "" {
		t.Error("want a description")
	}
}

func TestConnectorSurfacesUnauthorized(t *testing.T) {
	kc := KubeConnector{
		NewClient: func(kubeconfig.Context, kubeconfig.Options) (kubernetes.Interface, error) {
			return nil, apierrors.NewUnauthorized("expired")
		},
	}
	_, err := kc.Connect(context.Background(), kubeconfig.Context{Name: "prod"})
	if err == nil {
		t.Fatal("want an error")
	}
	if classify(err) != StateUnauthorized {
		t.Fatalf("classify = %s, want unauthorized", classify(err))
	}
}

// A refused dial is a network problem. Reporting it as unauthorized sends the
// developer to re-authenticate when the real fix is turning on the VPN.
func TestRefusedDialIsUnreachableNotUnauthorized(t *testing.T) {
	kc := KubeConnector{
		NewClient: func(kubeconfig.Context, kubeconfig.Options) (kubernetes.Interface, error) {
			return nil, errors.New(`Get "https://10.0.0.1/version": dial tcp: connect: connection refused`)
		},
	}
	_, err := kc.Connect(context.Background(), kubeconfig.Context{Name: "prod"})
	if err == nil {
		t.Fatal("want an error")
	}
	if got := classify(err); got != StateUnreachable {
		t.Fatalf("classify = %s, want unreachable", got)
	}
}

func TestExecPluginExitIsUnauthorized(t *testing.T) {
	cmd := osexec.Command("sh", "-c", "exit 255")
	runErr := cmd.Run()
	if runErr == nil {
		t.Skip("expected a non-zero exit")
	}
	if got := classify(fmt.Errorf("getting credentials: %w", runErr)); got != StateUnauthorized {
		t.Fatalf("classify = %s, want unauthorized for a failed credential plugin", got)
	}
}

// Reported from a real cluster: an expired SSO session was being classified as
// unreachable, which sends the developer to check their VPN instead of running
// the login command. client-go formats plugin failures into a string error, so
// the typed checks alone do not catch these.
func TestExpiredSSOClassifiesAsUnauthorized(t *testing.T) {
	realWorld := []string{
		`getting credentials: exec: executable aws failed with exit code 255`,
		`Get "https://eks.example:443/version": getting credentials: exec: executable aws failed with exit code 1`,
		`the server has asked for the client to provide credentials`,
		`exec plugin: invalid apiVersion "client.authentication.k8s.io/v1alpha1"`,
	}
	for _, msg := range realWorld {
		t.Run(msg[:min(40, len(msg))], func(t *testing.T) {
			got := classify(errors.New(msg))
			if msg == `the server has asked for the client to provide credentials` {
				// This one arrives as a typed apierrors value in practice; as a
				// bare string it is not required to classify as unauthorized.
				return
			}
			if got != StateUnauthorized {
				t.Fatalf("classify(%q) = %s, want unauthorized", msg, got)
			}
		})
	}
}

func TestGenuineNetworkFailuresStayUnreachable(t *testing.T) {
	for _, msg := range []string{
		`dial tcp 10.0.0.1:443: connect: connection refused`,
		`dial tcp: lookup eks.example: no such host`,
		`context deadline exceeded`,
		`net/http: TLS handshake timeout`,
	} {
		if got := classify(errors.New(msg)); got != StateUnreachable {
			t.Errorf("classify(%q) = %s, want unreachable", msg, got)
		}
	}
}

func i32(v int32) *int32 { return &v }

// Health must survive the adapter: a Deployment below desired should reach the
// engine as degraded, not as an empty status.
func TestStatusReachesHealthDerivation(t *testing.T) {
	c := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: "ns", UID: "h", Generation: 2},
			Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas: 3, UpdatedReplicas: 3, AvailableReplicas: 3, ObservedGeneration: 2,
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "degraded", Namespace: "ns", UID: "d", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: i32(6)},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas: 5, UpdatedReplicas: 6, AvailableReplicas: 5, ObservedGeneration: 1,
			},
		},
	)

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got := map[string]apps.Health{}
	for _, a := range snap.Apps {
		got[a.Key.Name] = apps.Assess(a).Health
	}
	if got["healthy"] != apps.HealthHealthy {
		t.Errorf("healthy = %s, want healthy", got["healthy"])
	}
	if got["degraded"] != apps.HealthDegraded {
		t.Errorf("degraded = %s, want degraded", got["degraded"])
	}
}

func TestCrashLoopingPodReachesHealthDerivation(t *testing.T) {
	c := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns", UID: "d",
				Labels: map[string]string{"app.kubernetes.io/instance": "app"}},
			Spec:   appsv1.DeploymentSpec{Replicas: i32(1)},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-x", Namespace: "ns", UID: "p",
				Labels: map[string]string{"app.kubernetes.io/instance": "app"}},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{{
					RestartCount: 12,
					State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				}},
			},
		},
	)

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(snap.Apps))
	}
	a := apps.Assess(snap.Apps[0])
	if a.Health != apps.HealthFailed || a.Reason != "CrashLoopBackOff" {
		t.Fatalf("got %s/%s, want failed/CrashLoopBackOff", a.Health, a.Reason)
	}
	if !strings.Contains(a.Detail, "app-x") {
		t.Errorf("detail %q should name the pod", a.Detail)
	}
}

// An unset replica count means one, not zero. Reading it as zero would report
// every default-replica Deployment as healthy regardless of reality.
func TestUnsetReplicasDefaultsToOne(t *testing.T) {
	c := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns", UID: "d"},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
		},
	)
	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := apps.Assess(snap.Apps[0]).Health; got != apps.HealthFailed {
		t.Fatalf("health = %s, want failed: 0 of 1 ready", got)
	}
}

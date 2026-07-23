package clusters

import (
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"testing"

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

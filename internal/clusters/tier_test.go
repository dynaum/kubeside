package clusters

import (
	"context"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/kubeconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func tieredCluster() *fake.Clientset {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "team-a", UID: "dep"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "checkout"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "checkout:v1"}}},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout-1", Namespace: "team-a", Labels: map[string]string{"app": "checkout"},
		},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "checkout:v1"}}},
		Status: corev1.PodStatus{Phase: "Running"},
	}
	return fake.NewSimpleClientset(dep, pod)
}

func counted(c *fake.Clientset) map[string]int {
	seen := map[string]int{}
	c.PrependReactor("list", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		seen[a.GetResource().Resource]++
		return false, nil, nil
	})
	return seen
}

// The active tier is the environment on screen: everything it takes to render
// health, including pods.
func TestActiveTierReadsPods(t *testing.T) {
	c := tieredCluster()
	seen := counted(c)

	if _, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"}, FetchOptions{Tier: TierActive}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if seen["pods"] == 0 {
		t.Fatal("the active tier must read pods; health comes from them")
	}
}

// A background environment is one nobody is looking at. Reading its pods costs
// the most and answers the least: the promotion matrix needs versions, not
// per-replica health.
func TestBackgroundTierSkipsPods(t *testing.T) {
	c := tieredCluster()
	seen := counted(c)

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "stg"}, FetchOptions{Tier: TierBackground})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if seen["pods"] != 0 {
		t.Fatalf("the background tier read pods %d times", seen["pods"])
	}
	if len(snap.Apps) == 0 {
		t.Fatal("the background tier still has to produce apps")
	}
}

// An app read without its pods is still an app, and the view has to know that
// its health is unknown rather than fine.
func TestBackgroundTierSaysWhatItSkipped(t *testing.T) {
	c := tieredCluster()

	snap, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "stg"}, FetchOptions{Tier: TierBackground})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	found := false
	for _, p := range snap.Partial {
		if p == "Pod" {
			found = true
		}
	}
	if !found {
		t.Fatalf("partial = %v, should name pods as unread", snap.Partial)
	}
}

// Secrets are never listed by any tier. Their values must not enter a cache
// kubeside keeps, and the cheapest way to guarantee that is never to read them.
func TestNoTierListsSecrets(t *testing.T) {
	for _, tier := range []Tier{TierActive, TierBackground} {
		c := tieredCluster()
		seen := counted(c)
		if _, err := Fetch(context.Background(), c, kubeconfig.Context{Name: "qa"}, FetchOptions{Tier: tier}); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if seen["secrets"] != 0 {
			t.Fatalf("tier %v listed secrets %d times", tier, seen["secrets"])
		}
	}
}

// Attention keeps a connection alive. A tab watching an environment must not
// have it reaped underneath.
func TestTouchDefersReaping(t *testing.T) {
	fc := newConnector()
	m, clk := newManager(t, fc)

	if err := m.Connect(context.Background(), "qa"); err != nil {
		t.Fatal(err)
	}
	clk.advance(14 * time.Minute)
	m.Touch("qa")
	clk.advance(14 * time.Minute)

	if reaped := m.ReapIdle(); len(reaped) != 0 {
		t.Fatalf("reaped = %v; a watched context was dropped", reaped)
	}
}

// The tier a context sits in follows attention, and it is the manager that
// remembers which is which.
func TestTierFollowsAttention(t *testing.T) {
	fc := newConnector()
	m, _ := newManager(t, fc)

	if got := m.Tier("qa"); got != TierBackground {
		t.Fatalf("tier = %v, want background before anything is watched", got)
	}
	m.SetTier("qa", TierActive)
	if got := m.Tier("qa"); got != TierActive {
		t.Fatalf("tier = %v, want active once a tab is watching", got)
	}
	m.SetTier("qa", TierBackground)
	if got := m.Tier("qa"); got != TierBackground {
		t.Fatalf("tier = %v, want background once nobody is", got)
	}
}

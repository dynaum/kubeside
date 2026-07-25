package api

import (
	"context"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func clusterWithApp() *fake.Clientset {
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
	return fake.NewSimpleClientset(dep)
}

func serviceWith(t *testing.T, client *fake.Clientset, clock clusters.Clock) *Service {
	t.Helper()
	kcfg := &kubeconfig.Config{
		Current:  "qa1",
		Contexts: []kubeconfig.Context{{Name: "qa1", IsCurrent: true, Server: "https://api"}},
	}
	mgr := clusters.New(kcfg, clusters.KubeConnector{
		NewClient: func(kubeconfig.Context, kubeconfig.Options) (kubernetes.Interface, error) { return client, nil },
		// The production idle window, so demotion and disconnection stay
		// distinguishable in these tests exactly as they are in the product.
	}, clusters.Options{Clock: clock, IdleAfter: clusters.DefaultIdleAfter})
	t.Cleanup(mgr.Close)
	return NewService(kcfg, mgr, kubeconfig.Options{}, config.Empty(), time.Second)
}

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

// Reading an environment is attention: it belongs in the active tier, which is
// the one that reads pods.
func TestReadingAnEnvironmentPromotesItToActive(t *testing.T) {
	svc := serviceWith(t, clusterWithApp(), nil)

	if _, err := svc.Apps("qa1"); err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if got := svc.mgr.Tier("qa1"); got != clusters.TierActive {
		t.Fatalf("tier = %v, want active", got)
	}
}

// An idle-reaped context keeps what it last knew. The screen renders the old
// answer with its state, which is a different thing from knowing nothing.
func TestAStaleContextServesItsLastSnapshot(t *testing.T) {
	clock := &fixedClock{now: time.Unix(0, 0)}
	svc := serviceWith(t, clusterWithApp(), clock)

	first, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if len(first.Apps) == 0 {
		t.Fatal("the first read found nothing to remember")
	}

	clock.now = clock.now.Add(clusters.DefaultIdleAfter + time.Minute)
	if reaped := svc.Reap(); len(reaped) != 1 {
		t.Fatalf("reaped = %v, want the idle context", reaped)
	}

	got, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps after reaping: %v", err)
	}
	if len(got.Apps) != len(first.Apps) {
		t.Fatalf("apps = %d after reaping, want the retained snapshot of %d", len(got.Apps), len(first.Apps))
	}
}

// A read that fails does not erase what was known. The old answer beats an
// empty one, and the error travels with it.
func TestAFailedReadKeepsTheLastAnswerAndSaysWhatWentWrong(t *testing.T) {
	client := clusterWithApp()
	svc := serviceWith(t, client, nil)

	if _, err := svc.Apps("qa1"); err != nil {
		t.Fatalf("Apps: %v", err)
	}

	client.PrependReactor("list", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})

	got, err := svc.Apps("qa1")
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	if len(got.Apps) == 0 {
		t.Fatal("a failed read wiped the retained snapshot")
	}
}

// Reconnecting can mean new credentials, so permissions are asked again rather
// than served from before the connection dropped.
func TestReapingForgetsCachedPermissions(t *testing.T) {
	clock := &fixedClock{now: time.Unix(0, 0)}
	client := clusterWithApp()
	reviews := 0
	client.PrependReactor("create", "selfsubjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		reviews++
		return false, nil, nil
	})
	svc := serviceWith(t, client, clock)

	if _, err := svc.Apps("qa1"); err != nil {
		t.Fatal(err)
	}
	svc.Capabilities("qa1", "team-a")
	before := reviews

	clock.now = clock.now.Add(clusters.DefaultIdleAfter + time.Minute)
	svc.Reap()
	svc.Capabilities("qa1", "team-a")

	if reviews <= before {
		t.Fatal("permissions were served from before the reconnect")
	}
}

// Attention promotes and its absence demotes, without anything having to
// announce that a tab closed.
func TestAnUnwatchedEnvironmentFallsBackToBackground(t *testing.T) {
	clock := &fixedClock{now: time.Unix(0, 0)}
	svc := serviceWith(t, clusterWithApp(), clock)

	if _, err := svc.Apps("qa1"); err != nil {
		t.Fatal(err)
	}
	if svc.mgr.Tier("qa1") != clusters.TierActive {
		t.Fatal("reading should promote to active")
	}

	// Long enough to stop being the screen, short of the idle window.
	clock.now = clock.now.Add(activeWindow + time.Second)
	svc.Reap()

	if got := svc.mgr.Tier("qa1"); got != clusters.TierBackground {
		t.Fatalf("tier = %v, want background once nobody is reading it", got)
	}
	if st, _ := svc.mgr.Status("qa1"); st.State != clusters.StateLive {
		t.Fatalf("state = %v; demotion is not disconnection", st.State)
	}
}

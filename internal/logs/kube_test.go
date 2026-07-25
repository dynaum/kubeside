package logs

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func pod(name string, containers []string, init []string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a"}}
	for _, c := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
	}
	for _, c := range init {
		p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: c})
	}
	return p
}

func TestKubeTargetsCoverEveryContainerOfEveryPod(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("checkout-1", []string{"app", "istio-proxy"}, []string{"migrate"}),
		pod("checkout-2", []string{"app"}, nil),
		pod("unrelated-9", []string{"app"}, nil),
	)
	src := KubeSource{
		Client: client, Namespace: "team-a",
		PodNames: func(context.Context) ([]string, error) { return []string{"checkout-1", "checkout-2"}, nil },
	}

	targets, err := src.Targets(context.Background())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range targets {
		got[tg.Pod+"/"+tg.Container] = true
		if tg.Container == "migrate" && !tg.Init {
			t.Error("an init container is not marked as one")
		}
	}
	for _, want := range []string{"checkout-1/app", "checkout-1/istio-proxy", "checkout-1/migrate", "checkout-2/app"} {
		if !got[want] {
			t.Errorf("missing target %s", want)
		}
	}
	// A pod that is not part of this workload must not be streamed.
	if got["unrelated-9/app"] {
		t.Error("a pod outside the workload was targeted")
	}
}

func TestKubeTargetsWithNoPodsIsNotAnError(t *testing.T) {
	src := KubeSource{
		Client: fake.NewSimpleClientset(), Namespace: "team-a",
		PodNames: func(context.Context) ([]string, error) { return nil, nil },
	}

	targets, err := src.Targets(context.Background())
	if err != nil {
		t.Fatalf("Targets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("targets = %v, want none for a workload with no pods", targets)
	}
}

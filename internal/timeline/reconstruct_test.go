package timeline

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func crashedPod(t *testing.T, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team-a"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", RestartCount: 1,
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Reason: "Error", ExitCode: 1, FinishedAt: mt(t, "2026-07-24T11:00:00Z"),
					},
				},
			}},
		},
	}
}

func target() Target {
	return Target{Namespace: "team-a", Name: "checkout", Kind: "Deployment", UID: ownerUID, Pods: []string{"checkout-abc"}}
}

func TestReconstructAssemblesEverySource(t *testing.T) {
	rs1 := rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-20T10:00:00Z", 0)
	rs2 := rs(t, "checkout-2", "2", "checkout:1.1.0", "2026-07-24T09:00:00Z", 3)
	client := fake.NewSimpleClientset(
		&rs1, &rs2,
		crashedPod(t, "checkout-abc"),
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "team-a", CreationTimestamp: mt(t, "2026-07-24T12:00:00Z")},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Readiness probe failed",
			LastTimestamp:  mt(t, "2026-07-24T12:00:00Z"),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "checkout-abc"},
		},
	)

	tl := Reconstruct(context.Background(), client, target())

	kinds := map[string]int{}
	for _, e := range tl.Entries {
		kinds[e.Kind]++
	}
	if kinds[KindDeploy] != 2 || kinds[KindRestart] != 1 || kinds[KindWarning] != 1 {
		t.Fatalf("entries by kind = %v, want two deploys, a restart and a warning", kinds)
	}
	// Newest first: the warning at 12:00 leads.
	if tl.Entries[0].Kind != KindWarning {
		t.Errorf("first entry = %+v, want the newest", tl.Entries[0])
	}
	if len(tl.Gaps) != 0 {
		t.Errorf("gaps = %+v, want none when everything is readable", tl.Gaps)
	}
}

// The case that matters most: a read-only prod role that cannot read secrets.
// The timeline still renders, and it says what is missing rather than failing.
func TestForbiddenHelmSecretsDegradeToAGap(t *testing.T) {
	rs1 := rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-20T10:00:00Z", 3)
	client := fake.NewSimpleClientset(&rs1)
	client.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", nil)
	})

	tl := Reconstruct(context.Background(), client, target())

	if len(tl.Entries) == 0 {
		t.Fatal("a forbidden secret read wiped out the whole timeline")
	}
	if len(tl.Gaps) != 1 || tl.Gaps[0].Source != "helm" {
		t.Fatalf("gaps = %+v, want one naming helm", tl.Gaps)
	}
	if !contains(tl.Gaps[0].Reason, "read access to secrets") {
		t.Errorf("reason = %q, should name the access needed", tl.Gaps[0].Reason)
	}
}

func TestForbiddenRolloutsStillLeaveTheOtherSources(t *testing.T) {
	client := fake.NewSimpleClientset(crashedPod(t, "checkout-abc"))
	client.PrependReactor("list", "replicasets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "replicasets"}, "", nil)
	})

	tl := Reconstruct(context.Background(), client, target())

	if len(tl.Entries) != 1 || tl.Entries[0].Kind != KindRestart {
		t.Fatalf("entries = %+v, want the crash that is still readable", tl.Entries)
	}
	if len(tl.Gaps) != 1 || tl.Gaps[0].Source != "rollouts" {
		t.Fatalf("gaps = %+v", tl.Gaps)
	}
}

// A StatefulSet keeps its history somewhere else. Reading ReplicaSets for it
// would silently produce an empty axis.
func TestStatefulSetUsesControllerRevisions(t *testing.T) {
	client := fake.NewSimpleClientset(&appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-1", Namespace: "team-a", CreationTimestamp: mt(t, "2026-07-24T08:00:00Z"),
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "web", UID: ownerUID, Controller: ptr(true)}},
		},
		Revision: 7,
	})

	tgt := target()
	tgt.Kind = "StatefulSet"
	tgt.Name = "web"
	tgt.Pods = nil

	tl := Reconstruct(context.Background(), client, tgt)
	if len(tl.Entries) != 1 || tl.Entries[0].Revision != "7" {
		t.Fatalf("entries = %+v, want the controller revision", tl.Entries)
	}
}

// A bare pod has no rollout history at all. That is an empty source, not an
// error, and certainly not a crash.
func TestUnknownKindProducesNoRolloutSource(t *testing.T) {
	client := fake.NewSimpleClientset(crashedPod(t, "checkout-abc"))

	tgt := target()
	tgt.Kind = "Pod"

	tl := Reconstruct(context.Background(), client, tgt)
	if len(tl.Entries) != 1 {
		t.Fatalf("entries = %+v, want just the crash", tl.Entries)
	}
	for _, g := range tl.Gaps {
		if g.Source == "rollouts" {
			t.Errorf("a bare pod reported a rollout gap: %+v", g)
		}
	}
}

// Events belonging to another app in the same namespace are not this app's
// history.
func TestEventsOfOtherObjectsAreIgnored(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "team-a", CreationTimestamp: mt(t, "2026-07-24T12:00:00Z")},
		Type:           corev1.EventTypeWarning,
		Reason:         "Unhealthy",
		LastTimestamp:  mt(t, "2026-07-24T12:00:00Z"),
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "search-xyz"},
	})

	tl := Reconstruct(context.Background(), client, target())
	for _, e := range tl.Entries {
		if e.Kind == KindWarning {
			t.Fatalf("another app's warning landed here: %+v", e)
		}
	}
}

// The out-of-band change: somebody ran kubectl against prod, and the timeline
// says so instead of leaving it to be reconstructed by hand later.
func TestReconstructAttributesARolloutToKubectl(t *testing.T) {
	rs1 := rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-24T10:00:02Z", 3)
	editedAt := mt(t, "2026-07-24T10:00:00Z")
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "checkout", Namespace: "team-a", UID: ownerUID,
		ManagedFields: []metav1.ManagedFieldsEntry{
			{Manager: "kubectl-edit", Operation: metav1.ManagedFieldsOperationUpdate, Time: &editedAt},
		},
	}}
	client := fake.NewSimpleClientset(&rs1, dep)

	tl := Reconstruct(context.Background(), client, target())

	if len(tl.Entries) != 1 {
		t.Fatalf("entries = %+v", tl.Entries)
	}
	if tl.Entries[0].Actor.Kind != ActorKubectl || tl.Entries[0].Actor.Name != "kubectl-edit" {
		t.Fatalf("actor = %+v, want the kubectl edit that caused it", tl.Entries[0].Actor)
	}
}

// A workload the reader may not get is one missing attribution, not a broken
// timeline.
func TestReconstructWithoutTheWorkloadStillBuildsHistory(t *testing.T) {
	rs1 := rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-24T10:00:02Z", 3)
	client := fake.NewSimpleClientset(&rs1)
	client.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "checkout", nil)
	})

	tl := Reconstruct(context.Background(), client, target())

	if len(tl.Entries) != 1 {
		t.Fatalf("entries = %+v, want the rollout regardless", tl.Entries)
	}
	if len(tl.Gaps) != 1 || tl.Gaps[0].Source != "actors" {
		t.Fatalf("gaps = %+v, want one naming attribution", tl.Gaps)
	}
}

package timeline

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func mt(t *testing.T, s string) metav1.Time { return metav1.NewTime(at(t, s)) }

const ownerUID = types.UID("dep-uid")

func rs(t *testing.T, name, revision, image string, created string, replicas int32) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "team-a",
			CreationTimestamp: mt(t, created),
			Annotations:       map[string]string{"deployment.kubernetes.io/revision": revision},
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "Deployment", Name: "checkout", UID: ownerUID, Controller: ptr(true),
			}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }

// Deploy history is the single most valuable thing the cluster already holds
// and no tool assembles. Newest first, with the image that shipped.
func TestReplicaSetsBecomeDeployEntries(t *testing.T) {
	got, _ := FromReplicaSets([]appsv1.ReplicaSet{
		rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-20T10:00:00Z", 0),
		rs(t, "checkout-3", "3", "checkout:1.2.0", "2026-07-24T09:00:00Z", 4),
		rs(t, "checkout-2", "2", "checkout:1.1.0", "2026-07-22T11:00:00Z", 0),
	}, ownerUID)

	if len(got) != 3 {
		t.Fatalf("entries = %d, want one per revision", len(got))
	}
	if got[0].Revision != "3" || got[0].Image != "checkout:1.2.0" {
		t.Fatalf("newest entry = %+v, want revision 3", got[0])
	}
	if got[0].Kind != KindDeploy {
		t.Errorf("kind = %q", got[0].Kind)
	}
	if !got[0].At.After(got[1].At) {
		t.Error("entries are not newest first")
	}
}

// A ReplicaSet belonging to another Deployment in the same namespace is not
// this app's history. Merging them would invent rollouts.
func TestReplicaSetsOfOtherOwnersAreIgnored(t *testing.T) {
	other := rs(t, "search-1", "1", "search:2.0.0", "2026-07-23T10:00:00Z", 3)
	other.OwnerReferences[0].UID = "another-uid"

	got, _ := FromReplicaSets([]appsv1.ReplicaSet{
		rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-20T10:00:00Z", 3),
		other,
	}, ownerUID)

	if len(got) != 1 || got[0].Image != "checkout:1.0.0" {
		t.Fatalf("entries = %+v, want only this app's rollout", got)
	}
}

// Reconstruction runs out where revisionHistoryLimit pruned. Saying so is the
// difference between "nothing happened" and "we cannot see that far".
func TestReplicaSetsReportTheirHorizon(t *testing.T) {
	_, horizon := FromReplicaSets([]appsv1.ReplicaSet{
		rs(t, "checkout-9", "9", "checkout:1.9.0", "2026-07-24T09:00:00Z", 4),
		rs(t, "checkout-8", "8", "checkout:1.8.0", "2026-07-23T09:00:00Z", 0),
	}, ownerUID)

	if horizon == nil {
		t.Fatal("no horizon reported")
	}
	if !horizon.At.Equal(at(t, "2026-07-23T09:00:00Z")) {
		t.Errorf("horizon at %v, want the oldest surviving revision", horizon.At)
	}
	if horizon.Reason == "" || !contains(horizon.Reason, "revisionHistoryLimit") {
		t.Errorf("reason = %q, should name what pruned the rest", horizon.Reason)
	}
}

// Revision 1 still on the cluster means nothing was pruned: the horizon is the
// app's own beginning, and claiming otherwise would be a lie in the other
// direction.
func TestFirstRevisionMeansNothingWasPruned(t *testing.T) {
	_, horizon := FromReplicaSets([]appsv1.ReplicaSet{
		rs(t, "checkout-1", "1", "checkout:1.0.0", "2026-07-20T10:00:00Z", 3),
	}, ownerUID)

	if horizon == nil {
		t.Fatal("no horizon reported")
	}
	if contains(horizon.Reason, "pruned") {
		t.Errorf("reason = %q, but the first revision is still here", horizon.Reason)
	}
}

func TestControllerRevisionsBecomeDeployEntries(t *testing.T) {
	got, horizon := FromControllerRevisions([]appsv1.ControllerRevision{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-6f8", CreationTimestamp: mt(t, "2026-07-22T08:00:00Z"),
				OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "web", UID: ownerUID, Controller: ptr(true)}},
			},
			Revision: 4,
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-9a1", CreationTimestamp: mt(t, "2026-07-24T08:00:00Z"),
				OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "web", UID: ownerUID, Controller: ptr(true)}},
			},
			Revision: 5,
		},
	}, ownerUID)

	if len(got) != 2 || got[0].Revision != "5" {
		t.Fatalf("entries = %+v, want newest revision first", got)
	}
	if horizon == nil || !contains(horizon.Reason, "revisionHistoryLimit") {
		t.Errorf("horizon = %+v, should name the pruning limit", horizon)
	}
}

func helmSecret(t *testing.T, name, version, status, created string) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "team-a",
			CreationTimestamp: mt(t, created),
			Labels: map[string]string{
				"owner": "helm", "name": "checkout", "version": version, "status": status,
			},
		},
		Type: "helm.sh/release.v1",
	}
}

// Helm history is the richest source and the one read-only prod roles most
// often forbid, so it has to work when it is there and degrade loudly when not.
func TestHelmSecretsBecomeReleaseEntries(t *testing.T) {
	got := FromHelmSecrets([]corev1.Secret{
		helmSecret(t, "sh.helm.release.v1.checkout.v2", "2", "superseded", "2026-07-22T10:00:00Z"),
		helmSecret(t, "sh.helm.release.v1.checkout.v3", "3", "deployed", "2026-07-24T10:00:00Z"),
	}, "checkout")

	if len(got) != 2 {
		t.Fatalf("entries = %d, want one per release", len(got))
	}
	if got[0].Kind != KindRelease || got[0].Revision != "3" {
		t.Fatalf("newest entry = %+v", got[0])
	}
	if !contains(got[0].Detail, "deployed") {
		t.Errorf("detail = %q, should carry the release status", got[0].Detail)
	}
	if got[0].Actor.Kind != ActorHelm {
		t.Errorf("actor = %+v, a Helm release was made by Helm", got[0].Actor)
	}
}

func TestHelmSecretsOfAnotherReleaseAreIgnored(t *testing.T) {
	other := helmSecret(t, "sh.helm.release.v1.search.v1", "1", "deployed", "2026-07-24T10:00:00Z")
	other.Labels["name"] = "search"

	got := FromHelmSecrets([]corev1.Secret{other}, "checkout")
	if len(got) != 0 {
		t.Fatalf("entries = %+v, want none for another release", got)
	}
}

func TestNonHelmSecretsAreIgnored(t *testing.T) {
	s := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-password", Namespace: "team-a"}}
	if got := FromHelmSecrets([]corev1.Secret{s}, "checkout"); len(got) != 0 {
		t.Fatalf("entries = %+v, want none", got)
	}
}

// A crash the developer did not see is exactly what the timeline is for. The
// exit code and reason survive in the pod, and one restart back is all the
// kubelet keeps.
func TestPodTerminationBecomesARestartEntry(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-abc", Namespace: "team-a"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 3,
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Reason: "OOMKilled", ExitCode: 137, FinishedAt: mt(t, "2026-07-24T11:00:00Z"),
					},
				},
			}},
		},
	}

	got := FromPods([]corev1.Pod{pod})
	if len(got) != 1 {
		t.Fatalf("entries = %d, want the last termination", len(got))
	}
	e := got[0]
	if e.Kind != KindRestart || !contains(e.Detail, "OOMKilled") || !contains(e.Detail, "137") {
		t.Fatalf("entry = %+v, want the reason and exit code", e)
	}
	if !e.At.Equal(at(t, "2026-07-24T11:00:00Z")) {
		t.Errorf("time = %v, want when it died", e.At)
	}
}

func TestPodWithoutATerminationProducesNothing(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-abc"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}},
		},
	}
	if got := FromPods([]corev1.Pod{pod}); len(got) != 0 {
		t.Fatalf("entries = %+v, want none for a healthy pod", got)
	}
}

func TestWarningEventsBecomeEntries(t *testing.T) {
	got, horizon := FromEvents([]corev1.Event{
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", CreationTimestamp: mt(t, "2026-07-24T12:00:00Z")},
			Type:           corev1.EventTypeWarning,
			Reason:         "Unhealthy",
			Message:        "Readiness probe failed: dial tcp i/o timeout",
			LastTimestamp:  mt(t, "2026-07-24T12:00:00Z"),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "checkout-abc"},
		},
		{
			ObjectMeta:     metav1.ObjectMeta{Name: "e2", CreationTimestamp: mt(t, "2026-07-24T12:05:00Z")},
			Type:           corev1.EventTypeNormal,
			Reason:         "Pulled",
			Message:        "Container image pulled",
			LastTimestamp:  mt(t, "2026-07-24T12:05:00Z"),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "checkout-abc"},
		},
	}, nil)

	if len(got) != 1 || got[0].Kind != KindWarning {
		t.Fatalf("entries = %+v, want only the warning", got)
	}
	// Events expire from the apiserver, so their horizon is separate from the
	// rollout horizon and has a different cause.
	if horizon == nil || !contains(horizon.Reason, "event") {
		t.Errorf("horizon = %+v, should explain the event TTL", horizon)
	}
}

func TestMergeOrdersEverythingNewestFirst(t *testing.T) {
	tl := Merge(
		[]Entry{{At: at(t, "2026-07-24T09:00:00Z"), Kind: KindDeploy, Title: "revision 3"}},
		[]Entry{{At: at(t, "2026-07-24T11:00:00Z"), Kind: KindRestart, Title: "app restarted"}},
		[]Entry{{At: at(t, "2026-07-24T10:00:00Z"), Kind: KindRelease, Title: "release 3"}},
	)

	if len(tl) != 3 {
		t.Fatalf("entries = %d", len(tl))
	}
	for i := 1; i < len(tl); i++ {
		if tl[i].At.After(tl[i-1].At) {
			t.Fatalf("not newest first: %v then %v", tl[i-1].At, tl[i].At)
		}
	}
}

// A forbidden source is a normal answer, not an error. The timeline renders
// from the others and names what is missing.
func TestGapsNameTheMissingSourceAndTheFix(t *testing.T) {
	tl := Timeline{}
	tl.AddGap("helm", forbidden("secrets"))

	if len(tl.Gaps) != 1 {
		t.Fatalf("gaps = %+v", tl.Gaps)
	}
	g := tl.Gaps[0]
	if !contains(g.Reason, "secrets") || !contains(g.Reason, "read") {
		t.Errorf("reason = %q, should say what access is missing", g.Reason)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The horizon is where the apiserver's retention ends, which is the oldest
// event in the namespace. Dating it from this app's oldest event would claim a
// cut where there is only a quiet app.
func TestEventHorizonUsesTheWholeNamespace(t *testing.T) {
	old := corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "other", CreationTimestamp: mt(t, "2026-07-24T08:00:00Z")},
		Type:           corev1.EventTypeWarning,
		Reason:         "BackOff",
		LastTimestamp:  mt(t, "2026-07-24T08:00:00Z"),
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "search-xyz"},
	}
	mine := corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "mine", CreationTimestamp: mt(t, "2026-07-24T12:00:00Z")},
		Type:           corev1.EventTypeWarning,
		Reason:         "Unhealthy",
		LastTimestamp:  mt(t, "2026-07-24T12:00:00Z"),
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "checkout-abc"},
	}

	got, horizon := FromEvents([]corev1.Event{old, mine}, func(e corev1.Event) bool {
		return e.InvolvedObject.Name == "checkout-abc"
	})

	if len(got) != 1 || got[0].Pod != "checkout-abc" {
		t.Fatalf("entries = %+v, want only this app's warning", got)
	}
	if horizon == nil || !horizon.At.Equal(at(t, "2026-07-24T08:00:00Z")) {
		t.Fatalf("horizon = %+v, want the oldest event in the namespace", horizon)
	}
}

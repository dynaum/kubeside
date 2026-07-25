package timeline

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mf(t *testing.T, manager, subresource, when string) metav1.ManagedFieldsEntry {
	e := metav1.ManagedFieldsEntry{Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate, Subresource: subresource}
	if when != "" {
		at := mt(t, when)
		e.Time = &at
	}
	return e
}

func TestClassifyManagerRecognizesTheUsualTools(t *testing.T) {
	cases := map[string]string{
		"kubectl-client-side-apply":     ActorKubectl,
		"kubectl-edit":                  ActorKubectl,
		"kubectl-rollout":               ActorKubectl,
		"helm":                          ActorHelm,
		"helm-controller":               ActorHelm,
		"argocd-controller":             ActorArgoCD,
		"argocd-application-controller": ActorArgoCD,
		"vpa-updater":                   ActorAutoscaler,
		"horizontal-pod-autoscaler":     ActorAutoscaler,
		"kube-controller-manager":       ActorController,
		"deployment-controller":         ActorController,
	}
	for manager, want := range cases {
		if got := classifyManager(manager); got != want {
			t.Errorf("classifyManager(%q) = %q, want %q", manager, got, want)
		}
	}
}

// An unrecognized manager is still information: it names whatever controller
// did this, which beats calling it unknown.
func TestUnrecognizedManagerKeepsItsName(t *testing.T) {
	a := actorFor(mf(t, "acme-deployer", "", "2026-07-24T10:00:00Z"))
	if a.Kind != ActorUnknown || a.Name != "acme-deployer" {
		t.Fatalf("actor = %+v, want the raw manager preserved", a)
	}
}

// A kubectl edit on prod is the out-of-band change this attribution exists to
// surface. Matching is by time: managedFields records when each manager last
// wrote, and a rollout that happened at that moment was that manager's doing.
func TestAttributeMatchesARolloutToItsManager(t *testing.T) {
	entries := []Entry{
		{At: at(t, "2026-07-24T10:00:05Z"), Kind: KindDeploy, Revision: "3"},
		{At: at(t, "2026-07-20T09:00:00Z"), Kind: KindDeploy, Revision: "2"},
	}
	fields := []metav1.ManagedFieldsEntry{
		mf(t, "kubectl-client-side-apply", "", "2026-07-24T10:00:00Z"),
		mf(t, "kube-controller-manager", "status", "2026-07-24T10:00:07Z"),
	}

	got := Attribute(entries, fields)

	if got[0].Actor.Kind != ActorKubectl {
		t.Fatalf("newest rollout actor = %+v, want kubectl", got[0].Actor)
	}
	// managedFields keeps only the latest entry per manager, so an older
	// rollout has no recoverable actor. Guessing one would be worse than none.
	if got[1].Actor.Kind != ActorUnknown || got[1].Actor.Name != "" {
		t.Errorf("older rollout actor = %+v, want none", got[1].Actor)
	}
}

// A status write is the controller reporting on the change, not the change.
// Attributing the rollout to it would bury whoever actually did it.
func TestStatusSubresourceIsNotTheActor(t *testing.T) {
	entries := []Entry{{At: at(t, "2026-07-24T10:00:02Z"), Kind: KindDeploy}}
	fields := []metav1.ManagedFieldsEntry{mf(t, "kube-controller-manager", "status", "2026-07-24T10:00:01Z")}

	got := Attribute(entries, fields)
	if got[0].Actor.Kind != ActorUnknown {
		t.Fatalf("actor = %+v, want none from a status write", got[0].Actor)
	}
}

// A manager that wrote hours before a rollout did not cause it.
func TestAttributeIgnoresAManagerOutsideTheWindow(t *testing.T) {
	entries := []Entry{{At: at(t, "2026-07-24T10:00:00Z"), Kind: KindDeploy}}
	fields := []metav1.ManagedFieldsEntry{mf(t, "kubectl-edit", "", "2026-07-24T06:00:00Z")}

	got := Attribute(entries, fields)
	if got[0].Actor.Kind != ActorUnknown {
		t.Fatalf("actor = %+v, want none: that write was four hours earlier", got[0].Actor)
	}
}

// Entries that already know who did it, like a Helm release, keep their actor.
func TestAttributeDoesNotOverwriteAKnownActor(t *testing.T) {
	entries := []Entry{{
		At: at(t, "2026-07-24T10:00:00Z"), Kind: KindRelease,
		Actor: Actor{Kind: ActorHelm, Name: "helm"},
	}}
	fields := []metav1.ManagedFieldsEntry{mf(t, "kubectl-edit", "", "2026-07-24T10:00:00Z")}

	got := Attribute(entries, fields)
	if got[0].Actor.Kind != ActorHelm {
		t.Fatalf("actor = %+v, want the release's own", got[0].Actor)
	}
}

func TestAttributePrefersTheClosestManager(t *testing.T) {
	entries := []Entry{{At: at(t, "2026-07-24T10:00:10Z"), Kind: KindDeploy}}
	fields := []metav1.ManagedFieldsEntry{
		mf(t, "argocd-controller", "", "2026-07-24T09:59:20Z"),
		mf(t, "kubectl-edit", "", "2026-07-24T10:00:08Z"),
	}

	got := Attribute(entries, fields)
	if got[0].Actor.Kind != ActorKubectl {
		t.Fatalf("actor = %+v, want the manager that wrote closest to the rollout", got[0].Actor)
	}
}

func TestAttributeToleratesAManagerWithNoTimestamp(t *testing.T) {
	entries := []Entry{{At: at(t, "2026-07-24T10:00:00Z"), Kind: KindDeploy}}
	got := Attribute(entries, []metav1.ManagedFieldsEntry{mf(t, "kubectl-edit", "", "")})
	if got[0].Actor.Kind != ActorUnknown {
		t.Fatalf("actor = %+v, want none when the write has no time", got[0].Actor)
	}
}

func TestAttributeWindowIsAMinute(t *testing.T) {
	if AttributionWindow != time.Minute {
		t.Errorf("window = %v; a rollout follows its trigger within seconds", AttributionWindow)
	}
}

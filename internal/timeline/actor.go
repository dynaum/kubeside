package timeline

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Who changed what, from managedFields.
//
// Kubernetes records the manager that last wrote each field, and when. That is
// enough to say a rollout came from kubectl rather than from CI, which is the
// difference between a change somebody can explain and a forensic exercise. A
// kubectl manager on prod is exactly the out-of-band change worth surfacing.
//
// The limits are real and are respected here. managedFields keeps only the
// latest entry per manager, so attribution reaches the most recent change and
// no further; older rollouts carry no actor rather than a guessed one.

// AttributionWindow is how close a manager's write must be to a rollout to be
// called its cause. A rollout follows its trigger within seconds.
const AttributionWindow = time.Minute

// ActorController is a Kubernetes controller acting on its own, which is not
// somebody to ask about the change.
const ActorController = "controller"

// ActorAutoscaler is an autoscaler, whose changes are expected rather than
// out-of-band.
const ActorAutoscaler = "autoscaler"

// managerRules map a field manager to a label. Prefix matching, because tools
// suffix their managers: kubectl-client-side-apply, kubectl-edit, and
// kubectl-rollout are all kubectl.
var managerRules = []struct {
	prefix string
	kind   string
}{
	{"kubectl", ActorKubectl},
	{"helm", ActorHelm},
	{"argocd", ActorArgoCD},
	{"argo-cd", ActorArgoCD},
	{"flux", ActorArgoCD},
	{"vpa", ActorAutoscaler},
	{"hpa", ActorAutoscaler},
	{"horizontal-pod-autoscaler", ActorAutoscaler},
	{"vertical-pod-autoscaler", ActorAutoscaler},
	{"cluster-autoscaler", ActorAutoscaler},
	{"kube-controller-manager", ActorController},
	{"deployment-controller", ActorController},
	{"replicaset-controller", ActorController},
	{"statefulset-controller", ActorController},
	{"daemonset-controller", ActorController},
}

// classifyManager normalizes a field manager. An unrecognized one classifies as
// unknown, and the caller keeps its raw name: naming the controller that did
// this beats calling it anonymous.
func classifyManager(manager string) string {
	m := strings.ToLower(manager)
	for _, r := range managerRules {
		if strings.HasPrefix(m, r.prefix) {
			return r.kind
		}
	}
	return ActorUnknown
}

func actorFor(e metav1.ManagedFieldsEntry) Actor {
	return Actor{Kind: classifyManager(e.Manager), Name: e.Manager}
}

// Attribute assigns an actor to the entries a manager can be matched to.
//
// Matching is by time: a rollout that happened when a manager wrote is that
// manager's doing. An entry outside the window keeps no actor, because a wrong
// name on a prod change is worse than no name.
func Attribute(entries []Entry, fields []metav1.ManagedFieldsEntry) []Entry {
	out := make([]Entry, len(entries))
	copy(out, entries)

	for i := range out {
		// An entry that already knows who did it, like a Helm release, is not
		// improved by guessing from field managers.
		if out[i].Actor.Kind != ActorUnknown || out[i].Actor.Name != "" {
			continue
		}
		if best, ok := closestManager(fields, out[i].At); ok {
			out[i].Actor = actorFor(best)
		}
	}
	return out
}

// closestManager finds the manager whose write is nearest the moment, within
// the window.
//
// Status writes are skipped: a controller reporting on a change is not the
// change, and attributing a rollout to it would bury whoever actually did it.
func closestManager(fields []metav1.ManagedFieldsEntry, when time.Time) (metav1.ManagedFieldsEntry, bool) {
	var best metav1.ManagedFieldsEntry
	found := false
	var closest time.Duration

	for _, f := range fields {
		if f.Subresource != "" || f.Time == nil {
			continue
		}
		d := when.Sub(f.Time.Time)
		if d < 0 {
			d = -d
		}
		if d > AttributionWindow {
			continue
		}
		if !found || d < closest {
			best, closest, found = f, d, true
		}
	}
	return best, found
}

package timeline

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Target is the workload to reconstruct.
type Target struct {
	Namespace string
	Name      string
	// Kind selects which rollout source applies: Deployments keep history in
	// ReplicaSets, StatefulSets and DaemonSets in ControllerRevisions.
	Kind string
	// UID is the workload's own UID, which is how history is attributed to
	// this app rather than to a same-named one that was deleted and recreated.
	UID types.UID
	// Pods are the pods currently backing the workload, from the grouping
	// engine.
	Pods []string
}

// Reconstruct assembles a target's history from what the cluster still holds.
//
// Every source is read independently and a failure in one is recorded as a gap
// rather than returned as an error. A read-only prod role that cannot read
// secrets still gets rollouts, crashes, and warnings, with one line explaining
// what Helm history would have added.
func Reconstruct(ctx context.Context, client kubernetes.Interface, t Target) Timeline {
	var tl Timeline
	var rollouts, releases, crashes, warnings []Entry

	switch t.Kind {
	case "Deployment", "Rollout":
		list, err := client.AppsV1().ReplicaSets(t.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			tl.AddGap("rollouts", describe(err, "replicasets"))
		} else {
			var h *Horizon
			rollouts, h = FromReplicaSets(list.Items, t.UID)
			tl.AddHorizon(h)
		}
	case "StatefulSet", "DaemonSet":
		list, err := client.AppsV1().ControllerRevisions(t.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			tl.AddGap("rollouts", describe(err, "controllerrevisions"))
		} else {
			var h *Horizon
			rollouts, h = FromControllerRevisions(list.Items, t.UID)
			tl.AddHorizon(h)
		}
	}

	// Helm history is the richest source and the one read-only prod roles most
	// often exclude, so it is read last and its absence is expected rather than
	// exceptional. The selector keeps the read to this release's secrets: no
	// value of any other secret is ever requested.
	secrets, err := client.CoreV1().Secrets(t.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm,name=" + t.Name,
	})
	if err != nil {
		tl.AddGap("helm", describe(err, "secrets"))
	} else {
		releases = FromHelmSecrets(secrets.Items, t.Name)
	}

	if len(t.Pods) > 0 {
		pods, err := client.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			tl.AddGap("crashes", describe(err, "pods"))
		} else {
			want := set(t.Pods)
			var mine []corev1.Pod
			for _, p := range pods.Items {
				if want[p.Name] {
					mine = append(mine, p)
				}
			}
			crashes = FromPods(mine)
		}
	}

	events, err := client.CoreV1().Events(t.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		tl.AddGap("events", describe(err, "events"))
	} else {
		want := set(append(append([]string{}, t.Pods...), t.Name))
		var h *Horizon
		warnings, h = FromEvents(events.Items, func(e corev1.Event) bool {
			return want[e.InvolvedObject.Name]
		})
		tl.AddHorizon(h)
	}

	// Attribution comes from the workload's own managedFields: Kubernetes
	// records which manager last wrote each field and when, and a rollout that
	// happened at that moment was that manager's doing.
	fields, err := managedFields(ctx, client, t)
	switch {
	case err == nil:
		rollouts = Attribute(rollouts, fields)
	case apierrors.IsNotFound(err):
		// The workload was deleted between the grouping read and this one.
		// Attribution is enrichment, so its absence is not a gap worth a line
		// on the screen.
	default:
		tl.AddGap("actors", describe(err, strings.ToLower(t.Kind)+"s"))
	}

	tl.Entries = Merge(rollouts, releases, crashes, warnings)
	return tl
}

// managedFields reads the workload object for its field managers. Nothing else
// from the object is used here: the history came from elsewhere, and this is
// only about who.
func managedFields(ctx context.Context, client kubernetes.Interface, t Target) ([]metav1.ManagedFieldsEntry, error) {
	get := metav1.GetOptions{}
	switch t.Kind {
	case "Deployment":
		o, err := client.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return nil, err
		}
		return o.ManagedFields, nil
	case "StatefulSet":
		o, err := client.AppsV1().StatefulSets(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return nil, err
		}
		return o.ManagedFields, nil
	case "DaemonSet":
		o, err := client.AppsV1().DaemonSets(t.Namespace).Get(ctx, t.Name, get)
		if err != nil {
			return nil, err
		}
		return o.ManagedFields, nil
	}
	// A kind kubeside does not model has no attribution path, which is an
	// absence rather than a failure.
	return nil, nil
}

// describe turns an API error into something a developer can act on. A refused
// read is a permission to request, not a failure to report.
func describe(err error, resource string) error {
	if apierrors.IsForbidden(err) {
		return forbidden(resource)
	}
	return err
}

func set(vals []string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

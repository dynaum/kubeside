// Package timeline reconstructs an application's history from the cluster.
//
// kubeside writes nothing to disk, so it records no history of its own. It does
// not need to: Kubernetes already retains substantial history in ReplicaSets,
// ControllerRevisions, Helm release secrets, pod termination states, and
// events. Nobody assembles it, which is the actual gap this package closes.
//
// Two rules govern everything here. Each source degrades independently: a
// forbidden Helm secret is a normal answer for a read-only prod role, so the
// timeline renders from the others and names what is missing. And where
// reconstruction runs out, it says why. Silence before a horizon means "not
// known", never "nothing happened", because rendering an empty axis as a quiet
// period misleads exactly the person under the most pressure.
package timeline

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Entry kinds.
const (
	KindDeploy  = "deploy"
	KindRelease = "release"
	KindRestart = "restart"
	KindScale   = "scale"
	KindWarning = "warning"
	// KindHealth is a live transition kubeside watched happen, rather than
	// something reconstructed from the cluster's own records.
	KindHealth = "health"
	// KindExec records somebody opening a shell in a container. A shell in
	// production is an event, not a page view.
	KindExec = "exec"
	// KindBreakGlass records somebody arming an environment for writes.
	// Arming production is an event, not a setting.
	KindBreakGlass = "break-glass"
	// KindReveal records that somebody read a secret value through kubeside.
	// Reading a production credential should leave a trace in the same place
	// every other change does.
	KindReveal  = "reveal"
	KindRun     = "run"
	KindSession = "session"
)

// Actor kinds. Attribution comes from managedFields where it is derivable.
const (
	ActorUnknown = ""
	ActorKubectl = "kubectl"
	ActorHelm    = "helm"
	ActorArgoCD  = "argocd"
	ActorHPA     = "hpa"
)

// Actor is who made a change, when the cluster can tell us.
type Actor struct {
	// Kind is the normalized label: kubectl, helm, argocd, hpa, or empty.
	Kind string `json:"kind,omitempty"`
	// Name is the raw field manager, kept because an unrecognized manager is
	// still information: it names the controller that did this.
	Name string `json:"name,omitempty"`
}

// Entry is one thing that happened.
type Entry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
	// Source names where this was reconstructed from, so a developer can tell
	// an assembled fact from a live one.
	Source   string `json:"source"`
	Revision string `json:"revision,omitempty"`
	Image    string `json:"image,omitempty"`
	Pod      string `json:"pod,omitempty"`
	Actor    Actor  `json:"actor,omitzero"`
}

// Horizon is where one source ran out, and why.
type Horizon struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Reason string    `json:"reason"`
	// Pruned is false when the source still holds the beginning of the app's
	// history, so the UI marks a start rather than a cut.
	Pruned bool `json:"pruned"`
}

// Gap is a source that could not be read at all.
type Gap struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// Timeline is the assembled history.
type Timeline struct {
	Entries  []Entry   `json:"entries"`
	Horizons []Horizon `json:"horizons,omitempty"`
	Gaps     []Gap     `json:"gaps,omitempty"`
}

// AddGap records a source that could not be read.
func (t *Timeline) AddGap(source string, err error) {
	if err == nil {
		return
	}
	t.Gaps = append(t.Gaps, Gap{Source: source, Reason: err.Error()})
}

// AddHorizon records where a source ran out.
func (t *Timeline) AddHorizon(h *Horizon) {
	if h != nil {
		t.Horizons = append(t.Horizons, *h)
	}
}

// Merge combines entries from every source, newest first.
func Merge(sets ...[]Entry) []Entry {
	var all []Entry
	for _, s := range sets {
		all = append(all, s...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	return all
}

// FromReplicaSets turns a Deployment's ReplicaSets into rollout history.
//
// Every ReplicaSet the cluster still holds is one revision that shipped, with
// the image it shipped. The horizon is the oldest survivor: everything before
// it was pruned by revisionHistoryLimit and is genuinely unknowable.
func FromReplicaSets(sets []appsv1.ReplicaSet, owner types.UID) ([]Entry, *Horizon) {
	var mine []appsv1.ReplicaSet
	for _, rs := range sets {
		if ownedBy(rs.OwnerReferences, owner) {
			mine = append(mine, rs)
		}
	}
	if len(mine) == 0 {
		return nil, nil
	}
	sort.Slice(mine, func(i, j int) bool {
		return mine[i].CreationTimestamp.Time.After(mine[j].CreationTimestamp.Time)
	})

	entries := make([]Entry, 0, len(mine))
	lowest := 0
	for _, rs := range mine {
		rev := rs.Annotations["deployment.kubernetes.io/revision"]
		if n, err := strconv.Atoi(rev); err == nil && (lowest == 0 || n < lowest) {
			lowest = n
		}
		entries = append(entries, Entry{
			At:       rs.CreationTimestamp.Time,
			Kind:     KindDeploy,
			Title:    revisionTitle(rev),
			Detail:   imageSummary(rs.Spec.Template.Spec.Containers),
			Source:   "replicaset",
			Revision: rev,
			Image:    firstImage(rs.Spec.Template.Spec.Containers),
		})
	}

	oldest := mine[len(mine)-1]
	return entries, &Horizon{
		At:     oldest.CreationTimestamp.Time,
		Source: "replicaset",
		Pruned: lowest > 1,
		Reason: prunedReason(lowest, "revisionHistoryLimit"),
	}
}

// FromControllerRevisions does the same for StatefulSets and DaemonSets, which
// keep their history as ControllerRevisions rather than ReplicaSets.
func FromControllerRevisions(revs []appsv1.ControllerRevision, owner types.UID) ([]Entry, *Horizon) {
	var mine []appsv1.ControllerRevision
	for _, cr := range revs {
		if ownedBy(cr.OwnerReferences, owner) {
			mine = append(mine, cr)
		}
	}
	if len(mine) == 0 {
		return nil, nil
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Revision > mine[j].Revision })

	entries := make([]Entry, 0, len(mine))
	for _, cr := range mine {
		rev := strconv.FormatInt(cr.Revision, 10)
		entries = append(entries, Entry{
			At:       cr.CreationTimestamp.Time,
			Kind:     KindDeploy,
			Title:    revisionTitle(rev),
			Source:   "controllerrevision",
			Revision: rev,
		})
	}

	oldest := mine[len(mine)-1]
	return entries, &Horizon{
		At:     oldest.CreationTimestamp.Time,
		Source: "controllerrevision",
		Pruned: oldest.Revision > 1,
		Reason: prunedReason(int(oldest.Revision), "revisionHistoryLimit"),
	}
}

// FromHelmSecrets reads release history from the secrets Helm writes.
//
// The labels carry everything the axis needs, so the gzipped release payload is
// never decoded: less to go wrong, and nothing sensitive is touched.
func FromHelmSecrets(secrets []corev1.Secret, release string) []Entry {
	var entries []Entry
	for _, s := range secrets {
		if s.Labels["owner"] != "helm" || s.Labels["name"] != release {
			continue
		}
		version := s.Labels["version"]
		status := s.Labels["status"]
		entries = append(entries, Entry{
			At:       s.CreationTimestamp.Time,
			Kind:     KindRelease,
			Title:    "helm release " + version,
			Detail:   strings.TrimSpace("status " + status),
			Source:   "helm",
			Revision: version,
			Actor:    Actor{Kind: ActorHelm, Name: "helm"},
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	return entries
}

// FromPods recovers crashes from what the kubelet still reports.
//
// lastState reaches exactly one termination back, so this is the last crash and
// never the one before it. The restart count says how many there have been,
// which is the honest way to say the rest are gone.
func FromPods(pods []corev1.Pod) []Entry {
	var entries []Entry
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			term := cs.LastTerminationState.Terminated
			if term == nil {
				continue
			}
			detail := fmt.Sprintf("%s, exit code %d", or(term.Reason, "terminated"), term.ExitCode)
			if cs.RestartCount > 1 {
				detail += fmt.Sprintf(" · %d restarts total, only the last is recoverable", cs.RestartCount)
			}
			entries = append(entries, Entry{
				At:     term.FinishedAt.Time,
				Kind:   KindRestart,
				Title:  cs.Name + " restarted",
				Detail: detail,
				Source: "pod",
				Pod:    p.Name,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	return entries
}

// FromEvents keeps the warnings still inside the apiserver's TTL.
//
// Normal events are dropped: a timeline of image pulls buries the probe failure
// that explains the incident.
//
// keep selects this app's events, but the horizon is computed over all of them.
// The oldest event in the namespace is where the apiserver's retention actually
// ends; the oldest event for one app is just when that app last had something
// to say, and dating the horizon there would claim a cut that is not real.
func FromEvents(events []corev1.Event, keep func(corev1.Event) bool) ([]Entry, *Horizon) {
	var entries []Entry
	oldest := time.Time{}
	for _, e := range events {
		when := eventTime(e)
		if oldest.IsZero() || when.Before(oldest) {
			oldest = when
		}
		if e.Type != corev1.EventTypeWarning {
			continue
		}
		if keep != nil && !keep(e) {
			continue
		}
		entries = append(entries, Entry{
			At:     when,
			Kind:   KindWarning,
			Title:  e.Reason,
			Detail: e.Message,
			Source: "event",
			Pod:    podName(e.InvolvedObject),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })

	if oldest.IsZero() {
		return entries, nil
	}
	return entries, &Horizon{
		At:     oldest,
		Source: "event",
		Pruned: true,
		Reason: "older events have expired from the apiserver, which keeps roughly an hour by default",
	}
}

func revisionTitle(rev string) string {
	if rev == "" {
		return "rolled out"
	}
	return "revision " + rev
}

func prunedReason(lowest int, limit string) string {
	if lowest > 1 {
		return fmt.Sprintf("older rollouts pruned by %s; revision %d is the oldest the cluster still holds", limit, lowest)
	}
	return "this is the first revision: the cluster still holds the whole history"
}

func imageSummary(cs []corev1.Container) string {
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.Image)
	}
	return strings.Join(names, ", ")
}

func firstImage(cs []corev1.Container) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].Image
}

func ownedBy(refs []metav1.OwnerReference, owner types.UID) bool {
	for _, r := range refs {
		if r.UID == owner {
			return true
		}
	}
	return false
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if e.EventTime.Time != (time.Time{}) && !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func podName(ref corev1.ObjectReference) string {
	if ref.Kind == "Pod" {
		return ref.Name
	}
	return ""
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// forbidden phrases a permission failure as the fix rather than the failure.
//
// "Helm history unavailable: needs read access to secrets" tells a developer
// what to ask for. A 403 tells them nothing they can act on.
func forbidden(resource string) error {
	return fmt.Errorf("needs read access to %s in this namespace", resource)
}

package api

import (
	"sort"
	"time"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/guard"
	"github.com/dynaum/kubeside/internal/metrics"
	"github.com/dynaum/kubeside/internal/promotion"
	"github.com/dynaum/kubeside/internal/rbac"
	"github.com/dynaum/kubeside/internal/resolved"
	"github.com/dynaum/kubeside/internal/timeline"
)

// The view types are the JSON contract with the browser. They are deliberately
// flat and self-describing: the UI renders health, scope, and partial reads
// directly, and never has to reconstruct meaning the backend already computed.

// ContextView is one kubeconfig context and its live connection state.
type ContextView struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	State   string `json:"state"`   // never-connected, connecting, live, stale, unreachable, unauthorized
	HasData bool   `json:"hasData"` // false means never render an app list; nothing is known
	AgeSec  int64  `json:"ageSec,omitempty"`
	Error   string `json:"error,omitempty"`

	// The environment this context belongs to, resolved from the config file
	// when one binds it and from the context name otherwise. Risk drives the
	// colour and the hazard hatch, so the browser never re-guesses what an
	// environment is from its name.
	Environment string `json:"environment"`
	Risk        string `json:"risk"`   // low, medium, high
	Color       string `json:"color"`  // green, amber, red, violet, or whatever the file named
	Hazard      bool   `json:"hazard"` // hatch the edge: high risk only
	Write       string `json:"write"`  // allow, confirm, deny, break-glass
}

// AppsView is one context's grouped app list plus the honesty metadata the UI
// needs: what scope was readable and which kinds were not.
type AppsView struct {
	Context string      `json:"context"`
	State   string      `json:"state"`
	Scope   string      `json:"scope"`
	Reason  string      `json:"reason,omitempty"`
	Partial []string    `json:"partial,omitempty"`
	Apps    []AppView   `json:"apps"`
	Error   string      `json:"error,omitempty"`
	Metrics MetricsInfo `json:"metrics"`
}

// MetricsInfo tells the UI whether to render usage columns at all. A source
// that is unavailable yields no columns, never zeroes.
type MetricsInfo struct {
	Source    string `json:"source"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// AppDetailView is everything Screen 2 renders: the app's current state, the
// pods behind it, and its history. One read, because a screen that arrives in
// four pieces flickers through four wrong answers first.
type AppDetailView struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Kind      string `json:"kind"`

	Health string `json:"health"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	Ready  string `json:"ready,omitempty"`

	// Image and RevisionAt come from the newest reconstructed rollout, which is
	// the only place the running version is recoverable without a second read.
	Image      string `json:"image,omitempty"`
	RevisionAt string `json:"revisionAt,omitempty"`

	Restarts int32        `json:"restarts"`
	Pods     []PodView    `json:"pods"`
	Timeline TimelineView `json:"timeline"`
}

// PodView is one replica.
type PodView struct {
	Name     string `json:"name"`
	Phase    string `json:"phase,omitempty"`
	Health   string `json:"health"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	AgeSec   int64  `json:"ageSec,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ConfigView is the configuration one container actually received, one table
// per container.
type ConfigView struct {
	Context    string               `json:"context"`
	Namespace  string               `json:"namespace"`
	Workload   string               `json:"workload"`
	Pod        string               `json:"pod"`
	Containers []resolved.Container `json:"containers"`
	Caveat     string               `json:"caveat,omitempty"`
	// ComparedTo names the revision the diff column compares against.
	ComparedTo string `json:"comparedTo,omitempty"`
}

// RevealView is one secret key, fetched on demand. It carries exactly the key
// that was asked for and nothing else from the object.
type RevealView struct {
	Secret string `json:"secret"`
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	// Binary marks data that is not text. Rendering bytes as characters would
	// produce garbage and hide what the value actually is.
	Binary bool   `json:"binary,omitempty"`
	Note   string `json:"note,omitempty"`
}

// DiffRequest names the two sides of a comparison. The right side defaults to
// the same namespace and workload, since the common case is the same app in
// another environment.
type DiffRequest struct {
	Context        string
	Namespace      string
	Workload       string
	Other          string
	OtherNamespace string
	OtherWorkload  string
	Container      string
}

// DiffSide is one environment in a comparison.
type DiffSide struct {
	Context   string       `json:"context"`
	Namespace string       `json:"namespace"`
	Workload  string       `json:"workload"`
	Pod       string       `json:"pod,omitempty"`
	Env       resolved.Env `json:"env"`
}

// DiffView is one app's configuration compared across two environments.
type DiffView struct {
	Left      DiffSide            `json:"left"`
	Right     DiffSide            `json:"right"`
	Container string              `json:"container"`
	Rows      []resolved.CrossRow `json:"rows"`
	Summary   resolved.Summary    `json:"summary"`
}

// ForwardRequest asks for one port-forward.
type ForwardRequest struct {
	Context    string `json:"context"`
	Namespace  string `json:"namespace"`
	Workload   string `json:"workload"`
	Pod        string `json:"pod,omitempty"`
	RemotePort int    `json:"remotePort"`
	LocalPort  int    `json:"localPort,omitempty"`
	// Confirm is what the developer typed, when the environment asks for it.
	Confirm string `json:"confirm,omitempty"`
}

// GateRequest asks what ceremony an action needs in one environment.
type GateRequest struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace"`
	Verb      string `json:"verb"`
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	// Unlock, when set, arms the environment with this reason before answering.
	Unlock string `json:"unlock,omitempty"`

	// Pod, Container and RemotePort are what the equivalent command needs to be
	// a command. They are optional: a port-forward picks its pod as the tunnel
	// opens, so the dialog that precedes it cannot name one, and then no
	// equivalent is shown rather than a wrong one.
	Pod        string `json:"pod,omitempty"`
	Container  string `json:"container,omitempty"`
	RemotePort int    `json:"remotePort,omitempty"`
}

// GateView is the answer, with everything a confirmation dialog must show:
// which environment, what it touches, and the equivalent command.
type GateView struct {
	Gate       guard.Gate      `json:"gate"`
	Permission rbac.Permission `json:"permission"`
}

// CapabilitiesView is what this reader may do in one namespace of one context.
//
// The UI asks once per screen and renders every control accordingly: visible,
// disabled where refused, and naming the verb it needs. Nothing is ever hidden
// for lack of permission.
type CapabilitiesView struct {
	Context   string                     `json:"context"`
	Namespace string                     `json:"namespace"`
	Can       map[string]rbac.Permission `json:"can"`
}

// PromotionView is the matrix: one row per app, one column per environment.
type PromotionView struct {
	Envs    []promotion.Env   `json:"envs"`
	Rows    []promotion.Row   `json:"rows"`
	Summary promotion.Summary `json:"summary"`
	// Unreachable names the environments that could not be read at all, so an
	// empty column reads as "not connected" rather than as "nothing deployed".
	Unreachable []string `json:"unreachable,omitempty"`
}

// TimelineView is one app's reconstructed history, plus the honesty metadata:
// where each source ran out and which could not be read at all.
type TimelineView struct {
	Context   string             `json:"context"`
	Namespace string             `json:"namespace"`
	Workload  string             `json:"workload"`
	Entries   []timeline.Entry   `json:"entries"`
	Horizons  []timeline.Horizon `json:"horizons,omitempty"`
	Gaps      []timeline.Gap     `json:"gaps,omitempty"`
}

// AppView is one row.
type AppView struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Health    string `json:"health"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	GroupedBy string `json:"groupedBy"`
	ManagedBy string `json:"managedBy,omitempty"`
	Ready     string `json:"ready"` // "5/6" or "" when not applicable
	Objects   int    `json:"objects"`

	// What version is running, how long it has been running, and whether it is
	// flapping. Three of the four questions start here, and answering them one
	// click deep meant opening every app to find the one that moved.
	//
	// Tag is the display value and Image the full reference, because two apps
	// running "1.4.2" from different registries are not running the same code.
	// Both are empty when nothing in the snapshot carried an image; the row
	// renders a dash rather than a guess.
	Image string `json:"image,omitempty"`
	Tag   string `json:"tag,omitempty"`

	// RevisionAt is when the newest object in the app appeared, which is what a
	// rollout creates. The workload's own age answers a different question: how
	// long the app has existed, not how long this version has been serving.
	// Empty when nothing carried a creation stamp.
	//
	// A moment, not an age, because diffApps compares rows by value. An age
	// recomputed from the wall clock would differ on every re-read and send the
	// entire list every five seconds. The browser subtracts.
	RevisionAt string `json:"revisionAt,omitempty"`

	// Restarts is the container restart total across the app's pods, for the
	// lifetime of those pods. Kubernetes does not expose a windowed count, so
	// the column is labelled for what it is rather than as the 24-hour figure
	// docs/03-product-spec.md asks for.
	//
	// Pods is how many were read. Zero restarts across zero pods is not an app
	// that never restarted, and the row renders a dash rather than a zero
	// nobody measured.
	Pods     int32 `json:"pods"`
	Restarts int32 `json:"restarts"`

	// What the app's replicas are using right now, summed, which is the number
	// to compare against the limit set on the workload.
	//
	// Measured is how many pods reported. Zero means no column, because a
	// source that has not answered for this app yet is not an app using
	// nothing. A measured zero is a real reading and renders as one; that
	// distinction is the whole reason this is a count and not a bool.
	//
	// Usage follows the cluster, so a busy app changes these on every re-read
	// and its row is patched every time. That is different from a field
	// following the wall clock: this one moves because something moved.
	CPUMilli    int64 `json:"cpuMilli,omitempty"`
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	Measured    int32 `json:"measured"`
}

// AppsFromSnapshot renders a snapshot into the wire view. The health verdict is
// computed here, once, so every consumer sees the same derivation.
//
// usage is keyed by "namespace/pod", as metrics.ByPod produces it. A nil map is
// the normal case for a cluster with no metrics source, and produces rows with
// nothing measured rather than rows reading zero.
func AppsFromSnapshot(snap clusters.Snapshot, state string, metricsInfo MetricsInfo, usage map[string]metrics.Sample) AppsView {
	out := AppsView{
		Context: snap.Context,
		State:   state,
		Scope:   snap.Scope.String(),
		Reason:  snap.Scope.Reason,
		Partial: snap.Partial,
		Metrics: metricsInfo,
		Apps:    make([]AppView, 0, len(snap.Apps)),
	}
	for _, a := range snap.Apps {
		h := apps.Assess(a)
		f := rowFacts(a)
		v := AppView{
			Namespace: a.Key.Namespace,
			Name:      a.Key.Name,
			Kind:      a.Kind,
			Health:    h.Health.String(),
			Reason:    h.Reason,
			Detail:    h.Detail,
			GroupedBy: a.Origin.String(),
			ManagedBy: a.ManagedBy,
			Ready:     readyRatio(a),
			Objects:   len(a.Workloads),
			Image:     f.image,
			Tag:       promotion.TagOf(f.image),
			Pods:      f.pods,
			Restarts:  f.restarts,
		}
		if !f.revisionAt.IsZero() {
			v.RevisionAt = f.revisionAt.UTC().Format(time.RFC3339)
		}
		v.CPUMilli, v.MemoryBytes, v.Measured = usageOf(a, usage)
		out.Apps = append(out.Apps, v)
	}
	return out
}

// usageOf sums what the app's own pods are using.
//
// The lookup is by pod identity rather than by namespace, because two apps in
// one namespace are the common case and adding a neighbour's usage to yours
// would be worse than showing none. A pod with no entry is skipped rather than
// added as zero: the source may not have reported it yet, and a total that
// silently counts a missing reading as idle is the failure this column exists
// to avoid.
func usageOf(a apps.App, usage map[string]metrics.Sample) (cpuMilli, memoryBytes int64, measured int32) {
	if len(usage) == 0 {
		return 0, 0, 0
	}
	for _, w := range a.Workloads {
		if w.Kind != "Pod" {
			continue
		}
		s, ok := usage[w.Namespace+"/"+w.Name]
		if !ok {
			continue
		}
		cpuMilli += s.CPUMilli
		memoryBytes += s.MemoryBytes
		measured++
	}
	return cpuMilli, memoryBytes, measured
}

// rowFacts reads the running version, the moment this revision appeared, and
// the restart total from the snapshot the list already holds. Everything here
// is present in one read, which is what keeps the column set affordable on a
// list that re-renders every few seconds across several clusters.
//
// This mirrors instanceOf, which answers the same question for the promotion
// matrix. They stay separate because a promotion cell also needs the digest
// from pod status, and a list of four hundred rows should not pay for it.
func rowFacts(a apps.App) rowSummary {
	var f rowSummary
	var podImage string
	for _, w := range a.Workloads {
		// A rollout creates objects. The newest one dates the revision, and a
		// missing stamp is skipped rather than read as the zero time.
		if !w.Created.IsZero() && w.Created.After(f.revisionAt) {
			f.revisionAt = w.Created
		}
		if w.Status == nil {
			continue
		}
		// The workload's spec beats a pod's, because a pod may still be running
		// the previous revision while a rollout is in flight. The row answers
		// "what was deployed", and the health column already says whether it
		// arrived.
		if w.Kind == a.Kind && w.Status.Image != "" {
			f.image = w.Status.Image
		}
		if w.Kind == "Pod" {
			f.pods++
			f.restarts += w.Status.RestartCount
			if podImage == "" && w.Status.Image != "" {
				podImage = w.Status.Image
			}
		}
	}
	// A pod's image is the fallback, for a kind whose own spec was not read.
	if f.image == "" {
		f.image = podImage
	}
	return f
}

type rowSummary struct {
	image      string
	revisionAt time.Time
	pods       int32
	restarts   int32
}

// readyRatio mirrors the CLI: the top-level workload's ready-over-desired, or
// empty when the kind has no replica semantics.
func readyRatio(a apps.App) string {
	for _, w := range a.Workloads {
		if w.Status == nil {
			continue
		}
		switch w.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Rollout":
			if w.Status.DesiredReplicas > 0 {
				return itoa(int(w.Status.ReadyReplicas)) + "/" + itoa(int(w.Status.DesiredReplicas))
			}
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// PodsOf renders an app's pods, worst first: the replica that needs a human
// should not be somewhere in the middle of six.
func PodsOf(a apps.App, now time.Time) []PodView {
	var out []PodView
	for _, w := range a.Workloads {
		if w.Kind != "Pod" {
			continue
		}
		v := PodView{Name: w.Name}
		if w.Status != nil {
			v.Phase = w.Status.Phase
			v.Ready = w.Status.Ready
			v.Restarts = w.Status.RestartCount
			v.Reason = or(w.Status.WaitingReason, w.Status.TerminatedReason)
		}
		if !w.Created.IsZero() {
			v.AgeSec = int64(now.Sub(w.Created).Seconds())
		}
		v.Health = podHealth(w.Status)
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i].Health) != rank(out[j].Health) {
			return rank(out[i].Health) < rank(out[j].Health)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// podHealth keeps the status channel consistent with the app list: shape and
// colour mean the same thing on every screen.
func podHealth(st *apps.Status) string {
	switch {
	case st == nil:
		return "unknown"
	case st.WaitingReason != "" || st.TerminatedReason == "OOMKilled":
		return "failed"
	case st.Phase == "Failed":
		return "failed"
	case st.Phase == "Pending":
		return "progressing"
	case st.Ready:
		return "healthy"
	case st.Phase == "Succeeded":
		return "healthy"
	}
	return "degraded"
}

func rank(health string) int {
	switch health {
	case "failed":
		return 0
	case "degraded":
		return 1
	case "progressing":
		return 2
	case "unknown":
		return 3
	}
	return 4
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

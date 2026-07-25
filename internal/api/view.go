package api

import (
	"sort"
	"time"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/guard"
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
}

// AppsFromSnapshot renders a snapshot into the wire view. The health verdict is
// computed here, once, so every consumer sees the same derivation.
func AppsFromSnapshot(snap clusters.Snapshot, state string, metrics MetricsInfo) AppsView {
	out := AppsView{
		Context: snap.Context,
		State:   state,
		Scope:   snap.Scope.String(),
		Reason:  snap.Scope.Reason,
		Partial: snap.Partial,
		Metrics: metrics,
		Apps:    make([]AppView, 0, len(snap.Apps)),
	}
	for _, a := range snap.Apps {
		h := apps.Assess(a)
		out.Apps = append(out.Apps, AppView{
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
		})
	}
	return out
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

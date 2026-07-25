package api

import (
	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
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

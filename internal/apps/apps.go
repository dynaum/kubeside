package apps

import "sort"

// Object is the minimal shape the grouping engine needs from a Kubernetes
// resource. Adapters build these from typed or unstructured objects, which
// keeps the engine a pure function and testable without an apiserver.
type Object struct {
	Kind        string
	Name        string
	Namespace   string
	UID         string
	Labels      map[string]string
	Annotations map[string]string
	Owners      []Owner
}

// Owner is a controller reference. Only controller owners are followed; a
// plain owner reference does not imply the composition we care about.
type Owner struct {
	Kind       string
	Name       string
	UID        string
	Controller bool
}

// Key identifies an application inside one cluster. Namespace is part of the
// identity: two teams may each ship an "api", and merging them would present
// unrelated services as one row.
type Key struct {
	Namespace string
	Name      string
}

// Origin records which rule in the precedence chain produced the app name. It
// is surfaced in the UI so grouping is explainable rather than magic, and it
// is the first thing to look at when grouping surprises someone.
type Origin int

const (
	// OriginRecommendedLabels: app.kubernetes.io/instance or /name.
	OriginRecommendedLabels Origin = iota
	// OriginHelm: meta.helm.sh/release-name annotation.
	OriginHelm
	// OriginArgo: argocd.argoproj.io/instance label.
	OriginArgo
	// OriginOwnerChain: walked up to a top-level controller.
	OriginOwnerChain
	// OriginWorkloadName: nothing else applied, the workload's own name.
	OriginWorkloadName
)

func (o Origin) String() string {
	switch o {
	case OriginRecommendedLabels:
		return "recommended-labels"
	case OriginHelm:
		return "helm-release"
	case OriginArgo:
		return "argocd-instance"
	case OriginOwnerChain:
		return "owner-chain"
	case OriginWorkloadName:
		return "workload-name"
	}
	return "unknown"
}

// App is a logical service: one or more top-level workloads that belong
// together, plus everything reachable from them.
type App struct {
	Key    Key
	Kind   string // primary workload kind, by kindRank
	Origin Origin

	// Component is app.kubernetes.io/name when present. Identity uses the
	// instance so two releases of one chart stay separate, while Component
	// carries the stable name that cross-environment matching needs later.
	Component string

	Workloads []Object
}

// Name is the app's display name.
func (a App) Name() string { return a.Key.Name }

// kindRank orders workload kinds when one app owns several. Lower wins.
var kindRank = map[string]int{
	"Deployment":  0,
	"Rollout":     1,
	"StatefulSet": 2,
	"DaemonSet":   3,
	"CronJob":     4,
	"Job":         5,
	"ReplicaSet":  6,
	"Pod":         7,
}

func rankOf(kind string) int {
	if r, ok := kindRank[kind]; ok {
		return r
	}
	return 99
}

// topLevelKinds are workloads a developer thinks of as "a thing I deployed".
var topLevelKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"CronJob":     true,
	"Rollout":     true,
}

// sortApps gives deterministic output, which matters because the app list is
// compared across runs and asserted in tests.
func sortApps(in []App) []App {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Key.Namespace != in[j].Key.Namespace {
			return in[i].Key.Namespace < in[j].Key.Namespace
		}
		return in[i].Key.Name < in[j].Key.Name
	})
	for i := range in {
		ws := in[i].Workloads
		sort.Slice(ws, func(a, b int) bool {
			if rankOf(ws[a].Kind) != rankOf(ws[b].Kind) {
				return rankOf(ws[a].Kind) < rankOf(ws[b].Kind)
			}
			return ws[a].Name < ws[b].Name
		})
	}
	return in
}

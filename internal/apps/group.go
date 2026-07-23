package apps

import "strings"

// Label and annotation keys the precedence chain reads.
const (
	labelInstance  = "app.kubernetes.io/instance"
	labelName      = "app.kubernetes.io/name"
	labelArgo      = "argocd.argoproj.io/instance"
	labelPodHash   = "pod-template-hash"
	annotHelmName  = "meta.helm.sh/release-name"
	maxOwnerWalk   = 16 // cycle guard; real chains are 2 to 3 deep
	replicaSetKind = "ReplicaSet"
)

// Group turns a set of Kubernetes objects into applications.
//
// It is a pure function: no I/O, no clock, no randomness. The same input
// always produces the same output in the same order, which is what makes the
// engine testable against fixture clusters and what makes the kill criterion
// in issue #5 a fair judgement.
//
// The precedence chain is documented in docs/03-product-spec.md. Each object
// resolves to a top-level workload by following controller owner references,
// then that workload's identity is derived by the first rule that matches.
func Group(objects []Object) []App {
	if len(objects) == 0 {
		return nil
	}

	byUID := make(map[string]Object, len(objects))
	for _, o := range objects {
		byUID[o.UID] = o
	}

	byKey := make(map[Key]*App)
	var order []Key

	for _, o := range objects {
		root, origin := resolveRoot(o, byUID)
		key, comp, nameOrigin := identify(root, origin)

		app, ok := byKey[key]
		if !ok {
			app = &App{Key: key, Origin: nameOrigin, Component: comp}
			byKey[key] = app
			order = append(order, key)
		}

		// The strongest origin seen wins, so one well-labelled workload
		// explains the whole app rather than whichever object arrived first.
		if nameOrigin < app.Origin {
			app.Origin = nameOrigin
		}
		if app.Component == "" {
			app.Component = comp
		}
		app.Workloads = append(app.Workloads, o)

		if app.Kind == "" || rankOf(o.Kind) < rankOf(app.Kind) {
			app.Kind = o.Kind
		}
	}

	out := make([]App, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return sortApps(out)
}

// resolveRoot walks controller owner references up to the object a developer
// would call "the thing I deployed".
//
// Two cases matter and are easy to get wrong:
//
//   - The owner is present in the result set: follow it.
//   - The owner is absent, which is the norm under namespace-scoped RBAC: the
//     owner reference still carries its name, so use that rather than
//     inventing one app per ReplicaSet revision.
//
// The returned Origin describes how the ROOT'S NAME became available, not how
// far the walk travelled. Reaching a Deployment from a Pod and then using the
// Deployment's own name is still OriginWorkloadName; only a root synthesised
// from an owner reference we could not read is OriginOwnerChain.
func resolveRoot(o Object, byUID map[string]Object) (Object, Origin) {
	cur := o
	const origin = OriginWorkloadName
	seen := map[string]bool{cur.UID: true}

	for i := 0; i < maxOwnerWalk; i++ {
		if topLevelKinds[cur.Kind] {
			return cur, origin
		}
		ref, ok := controllerRef(cur)
		if !ok {
			return cur, origin
		}
		owner, present := byUID[ref.UID]
		if !present {
			// Synthesise the owner from the reference. It is enough to name
			// the app, which is all this step needs to do.
			return Object{
				Kind:      ref.Kind,
				Name:      absentOwnerName(cur, ref),
				Namespace: cur.Namespace,
				UID:       ref.UID,
			}, OriginOwnerChain
		}
		if seen[owner.UID] {
			// Cycle. Kubernetes should never produce one, but a malformed
			// object must not hang the engine.
			return cur, origin
		}
		seen[owner.UID] = true
		cur = owner
	}
	return cur, origin
}

// absentOwnerName recovers a usable name for an owner outside the result set.
//
// A ReplicaSet is named "<deployment>-<pod-template-hash>". When the child pod
// carries that hash we can strip it and recover the Deployment name. Without
// the hash there is nothing safe to remove: guessing at a suffix would merge
// unrelated workloads, which is worse than an extra row.
func absentOwnerName(child Object, ref Owner) string {
	if ref.Kind != replicaSetKind {
		return ref.Name
	}
	hash := child.Labels[labelPodHash]
	if hash == "" {
		return ref.Name
	}
	if trimmed := strings.TrimSuffix(ref.Name, "-"+hash); trimmed != "" && trimmed != ref.Name {
		return trimmed
	}
	return ref.Name
}

func controllerRef(o Object) (Owner, bool) {
	for _, ow := range o.Owners {
		if ow.Controller {
			return ow, true
		}
	}
	return Owner{}, false
}

// identify applies the precedence chain to a resolved root workload.
func identify(root Object, chainOrigin Origin) (Key, string, Origin) {
	ns := root.Namespace
	component := strings.TrimSpace(root.Labels[labelName])

	if v := strings.TrimSpace(root.Labels[labelInstance]); v != "" {
		return Key{ns, v}, component, OriginRecommendedLabels
	}
	if component != "" {
		return Key{ns, component}, component, OriginRecommendedLabels
	}
	if v := strings.TrimSpace(root.Annotations[annotHelmName]); v != "" {
		return Key{ns, v}, component, OriginHelm
	}
	if v := strings.TrimSpace(root.Labels[labelArgo]); v != "" {
		return Key{ns, v}, component, OriginArgo
	}
	return Key{ns, root.Name}, component, chainOrigin
}

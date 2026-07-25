package resolved

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// Comparing configuration across revisions, scoped to what actually survives.
//
// Old ReplicaSets keep the pod template, so anything declared there is
// recoverable: inline values, and which ConfigMap or Secret a key referenced.
// The contents of those ConfigMaps and Secrets are not. Kubernetes keeps no
// history for them and kubeside writes nothing to disk, so there is nowhere to
// read the old value from.
//
// Hence the rule this file exists to enforce: those cells say "not recoverable
// at this revision" and never "unchanged". Claiming a value held steady when
// nobody can know is the one error this column must not make, because it is the
// error that ends a debugging session in the wrong place.

// Diff states.
const (
	// DiffUnknown means no comparison was possible: there is no previous
	// revision, or this container did not exist in it.
	DiffUnknown = ""
	// DiffUnchanged is only ever set for values the template fully determines.
	DiffUnchanged      = "unchanged"
	DiffChanged        = "changed"
	DiffAdded          = "added"
	DiffRemoved        = "removed"
	DiffSourceChanged  = "source-changed"
	DiffNotRecoverable = "not-recoverable"
	// DiffRuntime marks values resolved at container start, which belong to the
	// run rather than to the revision.
	DiffRuntime = "runtime"
)

// Diff is how one value compares with the previous revision.
type Diff struct {
	State string `json:"state,omitempty"`
	// Previous is the old value, or the old reference when only that survives.
	Previous string `json:"previous,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Declared is one env entry as the pod template declares it. Declared, not
// resolved: a template holds references, never their contents.
type Declared struct {
	Key    string
	Value  string
	Source Source
}

// Snapshot is a revision's declared environment, by container.
type Snapshot struct {
	Containers map[string][]Declared
	// EnvFrom records the bulk references a container declared. Their keys
	// cannot be listed from the template, since the keys live in the object
	// being referenced, but knowing the reference existed is enough to tell a
	// genuinely new key from one whose old content is simply gone.
	EnvFrom map[string][]Source
}

// FromTemplate reads a revision's pod template. No cluster reads happen here:
// everything it returns was preserved inside the ReplicaSet.
func FromTemplate(containers, initContainers []corev1.Container) Snapshot {
	s := Snapshot{Containers: map[string][]Declared{}, EnvFrom: map[string][]Source{}}
	for _, c := range append(append([]corev1.Container{}, containers...), initContainers...) {
		var declared []Declared
		for _, e := range c.Env {
			declared = append(declared, declaredOf(e))
		}
		s.Containers[c.Name] = declared

		for _, from := range c.EnvFrom {
			switch {
			case from.ConfigMapRef != nil:
				s.EnvFrom[c.Name] = append(s.EnvFrom[c.Name], Source{Kind: SourceConfigMap, Ref: from.ConfigMapRef.Name})
			case from.SecretRef != nil:
				s.EnvFrom[c.Name] = append(s.EnvFrom[c.Name], Source{Kind: SourceSecret, Ref: from.SecretRef.Name})
			}
		}
	}
	return s
}

func declaredOf(e corev1.EnvVar) Declared {
	d := Declared{Key: e.Name}
	switch {
	case e.ValueFrom == nil:
		d.Value, d.Source = e.Value, Source{Kind: SourceInline}
	case e.ValueFrom.ConfigMapKeyRef != nil:
		ref := e.ValueFrom.ConfigMapKeyRef
		d.Source = Source{Kind: SourceConfigMap, Ref: ref.Name, Key: ref.Key}
	case e.ValueFrom.SecretKeyRef != nil:
		ref := e.ValueFrom.SecretKeyRef
		d.Source = Source{Kind: SourceSecret, Ref: ref.Name, Key: ref.Key}
	case e.ValueFrom.FieldRef != nil:
		d.Source = Source{Kind: SourceDownward, Key: e.ValueFrom.FieldRef.FieldPath}
	case e.ValueFrom.ResourceFieldRef != nil:
		d.Source = Source{Kind: SourceResource, Key: e.ValueFrom.ResourceFieldRef.Resource}
	}
	return d
}

// Compare annotates a resolved config against the previous revision.
//
// An empty snapshot leaves every row uncompared rather than marking everything
// added, because a screen full of "added" is noise dressed as information.
func Compare(cfg *Config, prev Snapshot, revision string) {
	if len(prev.Containers) == 0 {
		return
	}
	cfg.ComparedTo = revision

	for i := range cfg.Containers {
		c := &cfg.Containers[i]
		before, existed := prev.Containers[c.Name]
		if !existed {
			// This container is new in this revision. It has no previous self,
			// and comparing it to another container's environment would be
			// inventing a history.
			continue
		}

		was := map[string]Declared{}
		for _, d := range before {
			was[d.Key] = d
		}

		bulk := prev.EnvFrom[c.Name]
		for j := range c.Values {
			v := &c.Values[j]
			old, had := was[v.Key]
			delete(was, v.Key)
			v.Diff = compareOne(*v, old, had, bulk)
		}

		// A key the previous revision had and this one does not is a change,
		// and dropping it from the table would hide the change entirely.
		for _, d := range sortedRemaining(was) {
			c.Values = append(c.Values, removedValue(d))
		}
	}
}

func compareOne(v Value, old Declared, had bool, bulk []Source) Diff {
	// The downward API resolves at container start, so it belongs to the run
	// rather than to the revision. Comparing it means nothing.
	if v.Source.Kind == SourceDownward || v.Source.Kind == SourceResource {
		return Diff{State: DiffRuntime, Reason: "resolved when the container started, not part of the revision"}
	}
	if !had {
		// A key the previous template did not name individually may still have
		// come from an envFrom of the same object. Calling that "added" would
		// invent a change that did not happen.
		if from, ok := fromBulk(v.Source, bulk); ok {
			return Diff{
				State: DiffNotRecoverable,
				Reason: fmt.Sprintf(
					"this key comes from every key of %s %s; the keys that object held at the previous revision are not preserved",
					kindWord(from.Kind), from.Ref),
			}
		}
		return Diff{State: DiffAdded}
	}

	// The reference lives in the template, so a key that moved between
	// ConfigMaps is a real change even when neither content is recoverable.
	if v.Source.Kind != old.Source.Kind || v.Source.Ref != old.Source.Ref || v.Source.Key != old.Source.Key {
		return Diff{State: DiffSourceChanged, Previous: describe(old)}
	}

	if v.Source.Kind == SourceInline {
		if v.Value == old.Value {
			return Diff{State: DiffUnchanged}
		}
		return Diff{State: DiffChanged, Previous: old.Value}
	}

	// Same reference, and the content is genuinely gone.
	return Diff{
		State:  DiffNotRecoverable,
		Reason: fmt.Sprintf("Kubernetes keeps no content history for %s %s, so this value cannot be compared", kindWord(v.Source.Kind), v.Source.Ref),
	}
}

// fromBulk reports whether a value could have come from one of the previous
// revision's envFrom references.
func fromBulk(src Source, bulk []Source) (Source, bool) {
	for _, b := range bulk {
		if b.Kind == src.Kind && b.Ref == src.Ref {
			return b, true
		}
	}
	return Source{}, false
}

func removedValue(d Declared) Value {
	return Value{
		Key:    d.Key,
		Source: d.Source,
		Diff:   Diff{State: DiffRemoved, Previous: d.Value, Reason: "this key was in the previous revision and is not in this one"},
	}
}

// describe renders a reference the way the table names sources, so a previous
// value and a previous reference read the same way.
func describe(d Declared) string {
	if d.Source.Kind == SourceInline {
		return d.Value
	}
	if d.Source.Ref == "" {
		return d.Source.Kind
	}
	return d.Source.Kind + " " + d.Source.Ref
}

func kindWord(kind string) string {
	if kind == SourceSecret {
		return "Secret"
	}
	return "ConfigMap"
}

func sortedRemaining(was map[string]Declared) []Declared {
	out := make([]Declared, 0, len(was))
	for _, d := range was {
		out = append(out, d)
	}
	// Map order is random; a table that reshuffles between reads is unreadable.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

package resolved

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func declared(name string, env []corev1.EnvVar) []corev1.Container {
	return []corev1.Container{{Name: name, Env: env}}
}

func diffOf(t *testing.T, cfg *Config, container, key string) Diff {
	t.Helper()
	for _, c := range cfg.Containers {
		if c.Name != container {
			continue
		}
		for _, v := range c.Values {
			if v.Key == key {
				return v.Diff
			}
		}
	}
	t.Fatalf("no value %s in container %s", key, container)
	return Diff{}
}

func TestInlineValueThatChangedIsRecoverable(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "app",
		Values: []Value{{Key: "LOG_LEVEL", Value: "debug", Source: Source{Kind: SourceInline}}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}), nil)

	Compare(cfg, prev, "4")

	d := diffOf(t, cfg, "app", "LOG_LEVEL")
	if d.State != DiffChanged || d.Previous != "info" {
		t.Fatalf("diff = %+v, want the old inline value", d)
	}
}

func TestInlineValueThatHeldSteadyIsUnchanged(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "app",
		Values: []Value{{Key: "RETRY_LIMIT", Value: "3", Source: Source{Kind: SourceInline}}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{Name: "RETRY_LIMIT", Value: "3"}}), nil)

	Compare(cfg, prev, "4")

	if d := diffOf(t, cfg, "app", "RETRY_LIMIT"); d.State != DiffUnchanged {
		t.Fatalf("diff = %+v, want unchanged", d)
	}
}

func TestAKeyThatDidNotExistBeforeIsAdded(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "app",
		Values: []Value{{Key: "NEW_FLAG", Value: "true", Source: Source{Kind: SourceInline}}},
	}}}
	prev := FromTemplate(declared("app", nil), nil)

	Compare(cfg, prev, "4")

	if d := diffOf(t, cfg, "app", "NEW_FLAG"); d.State != DiffAdded {
		t.Fatalf("diff = %+v, want added", d)
	}
}

// The one error this column must not make. Kubernetes keeps no content history
// for ConfigMaps, so "unchanged" would be a claim nobody can support.
func TestConfigMapContentIsNotRecoverable(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "MAX_CONNECTIONS", Value: "25",
			Source: Source{Kind: SourceConfigMap, Ref: "payments-cfg", Key: "MAX_CONNECTIONS"},
		}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{
		Name: "MAX_CONNECTIONS",
		ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "payments-cfg"}, Key: "MAX_CONNECTIONS",
		}},
	}}), nil)

	Compare(cfg, prev, "4")

	d := diffOf(t, cfg, "app", "MAX_CONNECTIONS")
	if d.State != DiffNotRecoverable {
		t.Fatalf("diff = %+v; claiming a ConfigMap value held steady is the one thing this column must never do", d)
	}
	if d.Reason == "" {
		t.Error("a not-recoverable cell should say why")
	}
	if d.Previous != "" {
		t.Errorf("previous = %q, want nothing; the old content is genuinely gone", d.Previous)
	}
}

// The reference is in the pod template even when the content is not. A key that
// moved from one ConfigMap to another is a real, recoverable change.
func TestAChangedReferenceIsRecoverableEvenWhenContentIsNot(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "MAX_CONNECTIONS", Value: "25",
			Source: Source{Kind: SourceConfigMap, Ref: "payments-cfg-v2", Key: "MAX_CONNECTIONS"},
		}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{
		Name: "MAX_CONNECTIONS",
		ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "payments-cfg"}, Key: "MAX_CONNECTIONS",
		}},
	}}), nil)

	Compare(cfg, prev, "4")

	d := diffOf(t, cfg, "app", "MAX_CONNECTIONS")
	if d.State != DiffSourceChanged {
		t.Fatalf("diff = %+v, want the reference change surfaced", d)
	}
	if d.Previous != "configMap payments-cfg" {
		t.Errorf("previous = %q, want the old reference", d.Previous)
	}
}

// A value that stopped being inline and became a reference is the change that
// makes people say "but I set it to debug".
func TestInlineBecomingAReferenceIsASourceChange(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "LOG_LEVEL", Value: "info",
			Source: Source{Kind: SourceConfigMap, Ref: "payments-cfg", Key: "LOG_LEVEL"},
		}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}), nil)

	Compare(cfg, prev, "4")

	d := diffOf(t, cfg, "app", "LOG_LEVEL")
	if d.State != DiffSourceChanged || d.Previous != "debug" {
		t.Fatalf("diff = %+v, want the old inline value and a source change", d)
	}
}

// A secret's content was never read, so there is nothing to compare. Saying
// "unchanged" would be a claim built on a value nobody looked at.
func TestSecretValuesAreNeverCompared(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "PASSWORD", Masked: true,
			Source: Source{Kind: SourceSecret, Ref: "db", Key: "PASSWORD"},
		}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{
		Name: "PASSWORD",
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "PASSWORD",
		}},
	}}), nil)

	Compare(cfg, prev, "4")

	if d := diffOf(t, cfg, "app", "PASSWORD"); d.State != DiffNotRecoverable {
		t.Fatalf("diff = %+v, want not recoverable", d)
	}
}

// The downward API resolves at container start. It belongs to the run, not to
// the revision, and comparing it across revisions means nothing.
func TestDownwardAPIIsRuntimeNotRevisionScoped(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "POD_IP", Value: "10.4.19.221",
			Source: Source{Kind: SourceDownward, Key: "status.podIP"},
		}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{
		Name:      "POD_IP",
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}},
	}}), nil)

	Compare(cfg, prev, "4")

	if d := diffOf(t, cfg, "app", "POD_IP"); d.State != DiffRuntime {
		t.Fatalf("diff = %+v, want runtime", d)
	}
}

// With no previous revision there is nothing to compare against, and every row
// saying "added" would be noise dressed as information.
func TestNoPreviousRevisionLeavesEveryRowUncompared(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "app",
		Values: []Value{{Key: "A", Value: "1", Source: Source{Kind: SourceInline}}},
	}}}

	Compare(cfg, Snapshot{}, "")

	d := diffOf(t, cfg, "app", "A")
	if d.State != DiffUnknown {
		t.Fatalf("diff = %+v, want nothing claimed without a previous revision", d)
	}
}

// A container added in this revision has no previous self to compare with.
func TestAContainerThatDidNotExistBeforeIsUncompared(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "sidecar",
		Values: []Value{{Key: "A", Value: "1", Source: Source{Kind: SourceInline}}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{Name: "A", Value: "2"}}), nil)

	Compare(cfg, prev, "4")

	if d := diffOf(t, cfg, "sidecar", "A"); d.State != DiffUnknown {
		t.Fatalf("diff = %+v, want no claim for a container that is new", d)
	}
}

// A key the previous revision had and this one does not still belongs on the
// screen: its absence is the change.
func TestRemovedKeysAreListed(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "app",
		Values: []Value{{Key: "KEPT", Value: "1", Source: Source{Kind: SourceInline}}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{
		{Name: "KEPT", Value: "1"},
		{Name: "GONE", Value: "old"},
	}), nil)

	Compare(cfg, prev, "4")

	d := diffOf(t, cfg, "app", "GONE")
	if d.State != DiffRemoved || d.Previous != "old" {
		t.Fatalf("diff = %+v, want the removed key with its old value", d)
	}
}

func TestComparisonNamesTheRevisionItComparesAgainst(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name:   "app",
		Values: []Value{{Key: "A", Value: "1", Source: Source{Kind: SourceInline}}},
	}}}
	prev := FromTemplate(declared("app", []corev1.EnvVar{{Name: "A", Value: "0"}}), nil)

	Compare(cfg, prev, "4")

	if cfg.ComparedTo != "4" {
		t.Fatalf("comparedTo = %q, want the revision named", cfg.ComparedTo)
	}
}

// A key that arrives through envFrom is not named in the template, so the
// previous revision cannot list it. It is not new: its old content is simply
// gone, which is a different sentence.
func TestEnvFromKeysAreNotRecoverableRatherThanAdded(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "CFG_LOG_LEVEL", Value: "debug",
			Source: Source{Kind: SourceConfigMap, Ref: "payments-cfg", Key: "LOG_LEVEL"},
		}},
	}}}
	prev := FromTemplate([]corev1.Container{{
		Name: "app",
		EnvFrom: []corev1.EnvFromSource{{
			Prefix:       "CFG_",
			ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "payments-cfg"}},
		}},
		Env: []corev1.EnvVar{{Name: "OTHER", Value: "x"}},
	}}, nil)

	Compare(cfg, prev, "4")

	d := diffOf(t, cfg, "app", "CFG_LOG_LEVEL")
	if d.State != DiffNotRecoverable {
		t.Fatalf("diff = %+v, want not recoverable rather than added", d)
	}
}

// A key from an envFrom that the previous revision did not have at all really
// is new.
func TestKeyFromANewEnvFromIsAdded(t *testing.T) {
	cfg := &Config{Containers: []Container{{
		Name: "app",
		Values: []Value{{
			Key: "NEW_LOG_LEVEL", Value: "debug",
			Source: Source{Kind: SourceConfigMap, Ref: "brand-new-cfg", Key: "LOG_LEVEL"},
		}},
	}}}
	prev := FromTemplate([]corev1.Container{{
		Name:    "app",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "old-cfg"}}}},
		Env:     []corev1.EnvVar{{Name: "OTHER", Value: "x"}},
	}}, nil)

	Compare(cfg, prev, "4")

	if d := diffOf(t, cfg, "app", "NEW_LOG_LEVEL"); d.State != DiffAdded {
		t.Fatalf("diff = %+v, want added", d)
	}
}

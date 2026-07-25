package resolved

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func podWith(containers []corev1.Container, init []corev1.Container, volumes []corev1.Volume) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1", Namespace: "team-a"},
		Spec: corev1.PodSpec{
			Containers:     containers,
			InitContainers: init,
			Volumes:        volumes,
			NodeName:       "node-1",
		},
	}
}

func find(vals []Value, key string) (Value, bool) {
	for _, v := range vals {
		if v.Key == key {
			return v, true
		}
	}
	return Value{}, false
}

func TestInlineEnvIsResolvedWithItsSource(t *testing.T) {
	pod := podWith([]corev1.Container{{
		Name: "app",
		Env:  []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod), pod)

	if len(got.Containers) != 1 || got.Containers[0].Name != "app" {
		t.Fatalf("containers = %+v", got.Containers)
	}
	v, ok := find(got.Containers[0].Values, "LOG_LEVEL")
	if !ok {
		t.Fatal("LOG_LEVEL missing")
	}
	if v.Value != "debug" || v.Source.Kind != SourceInline {
		t.Fatalf("value = %+v, want the inline value with its provenance", v)
	}
}

func TestConfigMapKeyRefIsResolved(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-cfg", Namespace: "team-a"},
		Data:       map[string]string{"MAX_CONNECTIONS": "25"},
	}
	pod := podWith([]corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{
			Name: "MAX_CONNECTIONS",
			ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "payments-cfg"}, Key: "MAX_CONNECTIONS",
			}},
		}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod, cm), pod)

	v, _ := find(got.Containers[0].Values, "MAX_CONNECTIONS")
	if v.Value != "25" {
		t.Fatalf("value = %q, want the ConfigMap's", v.Value)
	}
	if v.Source.Kind != SourceConfigMap || v.Source.Ref != "payments-cfg" || v.Source.Key != "MAX_CONNECTIONS" {
		t.Fatalf("source = %+v, want the ConfigMap and key it came from", v.Source)
	}
}

// A secret value is never read to render the table. Masking by not fetching is
// the only masking that cannot leak.
func TestSecretValuesAreNeverFetched(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "team-a"},
		Data:       map[string][]byte{"PASSWORD": []byte("hunter2")},
	}
	pod := podWith([]corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{
			Name: "PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "PASSWORD",
			}},
		}},
	}}, nil, nil)

	client := fake.NewSimpleClientset(pod, sec)
	reads := 0
	client.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		reads++
		return false, nil, nil
	})

	got := Resolve(context.Background(), client, pod)

	v, _ := find(got.Containers[0].Values, "PASSWORD")
	if !v.Masked {
		t.Fatal("a secret-sourced value is not masked")
	}
	if v.Value != "" {
		t.Fatalf("value = %q, want nothing; a masked value that carries its plaintext is not masked", v.Value)
	}
	if reads != 0 {
		t.Errorf("the secret was read %d times; masking means not fetching", reads)
	}
	if v.Source.Kind != SourceSecret || v.Source.Ref != "db" {
		t.Errorf("source = %+v, should still name where it comes from", v.Source)
	}
}

func TestEnvFromConfigMapExpandsEveryKey(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "team-a"},
		Data:       map[string]string{"A": "1", "B": "2"},
	}
	pod := podWith([]corev1.Container{{
		Name:    "app",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}}}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod, cm), pod)

	if len(got.Containers[0].Values) != 2 {
		t.Fatalf("values = %+v, want one per key", got.Containers[0].Values)
	}
	a, _ := find(got.Containers[0].Values, "A")
	if a.Value != "1" || a.Source.Kind != SourceConfigMap {
		t.Errorf("A = %+v", a)
	}
}

// envFrom with a prefix is a real pattern and a silent source of confusion when
// a tool ignores it.
func TestEnvFromPrefixIsApplied(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "team-a"},
		Data:       map[string]string{"HOST": "db.internal"},
	}
	pod := podWith([]corev1.Container{{
		Name: "app",
		EnvFrom: []corev1.EnvFromSource{{
			Prefix:       "DB_",
			ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}},
		}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod, cm), pod)
	if _, ok := find(got.Containers[0].Values, "DB_HOST"); !ok {
		t.Fatalf("values = %+v, want the prefixed name", got.Containers[0].Values)
	}
}

// Kubernetes resolves env after envFrom, so an inline value wins. Showing the
// other one would be showing a value the container never had.
func TestInlineEnvOverridesEnvFrom(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "team-a"},
		Data:       map[string]string{"LOG_LEVEL": "info"},
	}
	pod := podWith([]corev1.Container{{
		Name:    "app",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}}}},
		Env:     []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod, cm), pod)

	v, _ := find(got.Containers[0].Values, "LOG_LEVEL")
	if v.Value != "debug" || v.Source.Kind != SourceInline {
		t.Fatalf("value = %+v, want the inline one that actually applied", v)
	}
	if !v.Overrides {
		t.Error("an override that hid another source should say so")
	}
}

func TestDownwardAPIIsResolvedFromThePod(t *testing.T) {
	pod := podWith([]corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{
			{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			{Name: "NODE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
		},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod), pod)

	name, _ := find(got.Containers[0].Values, "POD_NAME")
	if name.Value != "checkout-1" || name.Source.Kind != SourceDownward {
		t.Fatalf("POD_NAME = %+v", name)
	}
	node, _ := find(got.Containers[0].Values, "NODE")
	if node.Value != "node-1" {
		t.Errorf("NODE = %+v", node)
	}
}

// A reference to a key that does not exist is why a container crashed. It has
// to render as missing, not as empty.
func TestAMissingKeyIsMarkedNotBlank(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-cfg", Namespace: "team-a"},
		Data:       map[string]string{"OTHER": "x"},
	}
	pod := podWith([]corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{
			Name: "MISSING",
			ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "payments-cfg"}, Key: "NOPE",
			}},
		}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod, cm), pod)

	v, _ := find(got.Containers[0].Values, "MISSING")
	if !v.Missing || v.Value != "" {
		t.Fatalf("value = %+v, want it marked missing", v)
	}
	if v.Reason == "" {
		t.Error("a missing key should say what was not there")
	}
}

// A ConfigMap the reader may not get is a row that says so, not a row that
// disappears.
func TestAForbiddenConfigMapIsMarkedUnreadable(t *testing.T) {
	pod := podWith([]corev1.Container{{
		Name: "app",
		Env: []corev1.EnvVar{{
			Name: "SECRETIVE",
			ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "locked"}, Key: "K",
			}},
		}},
	}}, nil, nil)
	client := fake.NewSimpleClientset(pod)
	client.PrependReactor("get", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "locked", nil)
	})

	got := Resolve(context.Background(), client, pod)

	v, _ := find(got.Containers[0].Values, "SECRETIVE")
	if !v.Missing {
		t.Fatalf("value = %+v, want it marked unreadable", v)
	}
	if v.Reason == "" || v.Value != "" {
		t.Errorf("value = %+v, should carry the reason and no value", v)
	}
}

// Init containers and sidecars are separate tables. Merging them would show a
// value one container has and another does not.
func TestInitContainersAreSeparateAndMarked(t *testing.T) {
	pod := podWith(
		[]corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "A", Value: "1"}}}},
		[]corev1.Container{{Name: "migrate", Env: []corev1.EnvVar{{Name: "B", Value: "2"}}}},
		nil,
	)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod), pod)

	if len(got.Containers) != 2 {
		t.Fatalf("containers = %d, want the app and its init container", len(got.Containers))
	}
	// The application container leads: it is what the developer came to read.
	if got.Containers[0].Name != "app" || got.Containers[0].Init {
		t.Errorf("first container = %+v, want the app", got.Containers[0])
	}
	if !got.Containers[1].Init {
		t.Errorf("init container not marked: %+v", got.Containers[1])
	}
}

// File-based config is config. A tool that only shows env vars sends people to
// kubectl for half the answer.
func TestMountedConfigVolumesAreListed(t *testing.T) {
	pod := podWith([]corev1.Container{{
		Name:         "app",
		VolumeMounts: []corev1.VolumeMount{{Name: "cfg", MountPath: "/etc/payments"}},
	}}, nil, []corev1.Volume{{
		Name:         "cfg",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "payments-files"}}},
	}})

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod), pod)

	mounts := got.Containers[0].Mounts
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v", mounts)
	}
	if mounts[0].Path != "/etc/payments" || mounts[0].Source.Ref != "payments-files" {
		t.Fatalf("mount = %+v", mounts[0])
	}
}

func TestSecretVolumesAreMarkedMasked(t *testing.T) {
	pod := podWith([]corev1.Container{{
		Name:         "app",
		VolumeMounts: []corev1.VolumeMount{{Name: "creds", MountPath: "/var/run/creds"}},
	}}, nil, []corev1.Volume{{
		Name:         "creds",
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-creds"}},
	}})

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod), pod)
	if !got.Containers[0].Mounts[0].Masked {
		t.Fatal("a secret volume is not marked masked")
	}
}

// Values are read from the ConfigMap as it is now. A container that started
// before somebody edited it is still running the old value, and the screen has
// to say so rather than quietly claim the new one.
func TestResolutionCarriesTheStalenessCaveat(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "team-a"}, Data: map[string]string{"A": "1"}}
	pod := podWith([]corev1.Container{{
		Name:    "app",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}},
	}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod, cm), pod)
	if got.Caveat == "" {
		t.Fatal("no caveat about reading the ConfigMap as it is now")
	}
}

// An env var with no source at all is still config the container received.
func TestEmptyInlineValueIsStillAValue(t *testing.T) {
	pod := podWith([]corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "EMPTY", Value: ""}}}}, nil, nil)

	got := Resolve(context.Background(), fake.NewSimpleClientset(pod), pod)
	v, ok := find(got.Containers[0].Values, "EMPTY")
	if !ok || v.Missing {
		t.Fatalf("value = %+v, want an empty value that is not missing", v)
	}
}

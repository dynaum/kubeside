// Package resolved answers what configuration a container actually received.
//
// Kubernetes spreads that answer across env, envFrom, valueFrom, the downward
// API, and mounted volumes, with precedence rules between them. Reading it back
// means opening the pod spec, then every ConfigMap it references, and applying
// the same precedence the kubelet did. That is the work this package does once
// so nobody has to do it by hand at two in the morning.
//
// Secret values are masked by never being fetched. Masking a value you already
// hold is a rendering decision; masking a value you never read is a property.
package resolved

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Where a value came from.
const (
	SourceInline    = "inline"
	SourceConfigMap = "configMap"
	SourceSecret    = "secret"
	SourceDownward  = "downwardAPI"
	SourceResource  = "resourceField"
)

// Source is one value's provenance. Without it a config table is a list of
// strings nobody can act on: knowing MAX_CONNECTIONS is 25 matters far less
// than knowing which ConfigMap to edit.
type Source struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
	Key  string `json:"key,omitempty"`
}

// Value is one resolved environment entry.
type Value struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source Source `json:"source"`
	// Masked marks a value that was never read, because it comes from a Secret.
	Masked bool `json:"masked,omitempty"`
	// Missing marks a reference that could not be resolved. A key that is not
	// there is frequently why a container is crashing, so it renders as missing
	// rather than as blank.
	Missing bool `json:"missing,omitempty"`
	// Overrides marks a value that shadowed another source, which is where
	// "but the ConfigMap says something else" comes from.
	Overrides bool   `json:"overrides,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// CanReveal reports whether this reader may fetch the masked value.
	// RevealReason names what is missing when they may not, because a control
	// is disabled and explained, never hidden.
	CanReveal    bool   `json:"canReveal,omitempty"`
	RevealReason string `json:"revealReason,omitempty"`
}

// Mount is file-based configuration. A tool that shows only environment
// variables sends people back to kubectl for half of their config.
type Mount struct {
	Path     string `json:"path"`
	Source   Source `json:"source"`
	Masked   bool   `json:"masked,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// Permit reports whether this reader may read one Secret, and what they need
// when they may not.
type Permit func(secret string) (allowed bool, reason string)

// Container is one container's resolved configuration.
type Container struct {
	Name   string  `json:"name"`
	Init   bool    `json:"init,omitempty"`
	Image  string  `json:"image,omitempty"`
	Values []Value `json:"values"`
	Mounts []Mount `json:"mounts,omitempty"`
}

// Config is a pod's configuration, one table per container.
type Config struct {
	Pod        string      `json:"pod"`
	Containers []Container `json:"containers"`
	// Caveat states what this reading cannot promise.
	Caveat string `json:"caveat,omitempty"`
}

// The caveat is not a disclaimer, it is the truth about the reading: a
// container resolves its environment once, at start, and keeps those values
// until it restarts.
const staleCaveat = "values are read from the ConfigMaps as they are now; a container that started before a change is still running the old value until it restarts"

// Resolve reads one pod's configuration.
//
// Every ConfigMap is fetched at most once however many keys reference it, and
// no Secret is fetched at all.
func Resolve(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) Config {
	return ResolveWith(ctx, client, pod, nil)
}

// ResolveWith adds a permission probe, so every masked row knows whether its
// reveal would work before anybody clicks it.
func ResolveWith(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, permit Permit) Config {
	r := &resolver{
		ctx: ctx, client: client, pod: pod,
		maps: map[string]*configMap{}, permit: permit, permits: map[string]permitResult{},
	}

	out := Config{Pod: pod.Name, Caveat: staleCaveat}
	// The application containers lead: they are what the developer opened the
	// screen to read. Init containers follow, behind their own tab.
	for _, c := range pod.Spec.Containers {
		out.Containers = append(out.Containers, r.container(c, false))
	}
	for _, c := range pod.Spec.InitContainers {
		out.Containers = append(out.Containers, r.container(c, true))
	}
	return out
}

type configMap struct {
	data map[string]string
	err  error
}

type permitResult struct {
	allowed bool
	reason  string
}

type resolver struct {
	ctx     context.Context
	client  kubernetes.Interface
	pod     *corev1.Pod
	maps    map[string]*configMap
	permit  Permit
	permits map[string]permitResult
}

// mayReveal probes once per Secret, however many keys reference it.
func (r *resolver) mayReveal(secret string) (bool, string) {
	if r.permit == nil {
		return false, "reveal is not available in this build"
	}
	if p, ok := r.permits[secret]; ok {
		return p.allowed, p.reason
	}
	allowed, reason := r.permit(secret)
	r.permits[secret] = permitResult{allowed, reason}
	return allowed, reason
}

func (r *resolver) container(c corev1.Container, init bool) Container {
	out := Container{Name: c.Name, Init: init, Image: c.Image}

	// envFrom first, then env: the same order the kubelet applies, so a value
	// shown here is the value the process got.
	byKey := map[string]int{}
	add := func(v Value) {
		if i, seen := byKey[v.Key]; seen {
			v.Overrides = true
			out.Values[i] = v
			return
		}
		byKey[v.Key] = len(out.Values)
		out.Values = append(out.Values, v)
	}

	for _, from := range c.EnvFrom {
		for _, v := range r.envFrom(from) {
			add(v)
		}
	}
	for _, e := range c.Env {
		add(r.env(e))
	}

	sort.SliceStable(out.Values, func(i, j int) bool { return out.Values[i].Key < out.Values[j].Key })
	out.Mounts = r.mounts(c)
	return out
}

func (r *resolver) envFrom(from corev1.EnvFromSource) []Value {
	switch {
	case from.ConfigMapRef != nil:
		name := from.ConfigMapRef.Name
		cm := r.configMap(name)
		if cm.err != nil {
			return []Value{{
				Key:     from.Prefix + "*",
				Source:  Source{Kind: SourceConfigMap, Ref: name},
				Missing: true,
				Reason:  unreadable("ConfigMap", name, cm.err),
			}}
		}
		out := make([]Value, 0, len(cm.data))
		for k, v := range cm.data {
			out = append(out, Value{
				Key:    from.Prefix + k,
				Value:  v,
				Source: Source{Kind: SourceConfigMap, Ref: name, Key: k},
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
		return out

	case from.SecretRef != nil:
		// The keys of a Secret cannot be listed without reading it, and reading
		// it is what masking avoids. The row names the Secret instead.
		allowed, reason := r.mayReveal(from.SecretRef.Name)
		return []Value{{
			Key:          from.Prefix + "*",
			Source:       Source{Kind: SourceSecret, Ref: from.SecretRef.Name},
			Masked:       true,
			Reason:       "every key of this Secret is in the environment; values are not read",
			CanReveal:    allowed,
			RevealReason: reason,
		}}
	}
	return nil
}

func (r *resolver) env(e corev1.EnvVar) Value {
	v := Value{Key: e.Name}

	switch {
	case e.ValueFrom == nil:
		v.Value = e.Value
		v.Source = Source{Kind: SourceInline}

	case e.ValueFrom.ConfigMapKeyRef != nil:
		ref := e.ValueFrom.ConfigMapKeyRef
		v.Source = Source{Kind: SourceConfigMap, Ref: ref.Name, Key: ref.Key}
		cm := r.configMap(ref.Name)
		switch {
		case cm.err != nil:
			v.Missing, v.Reason = true, unreadable("ConfigMap", ref.Name, cm.err)
		default:
			val, ok := cm.data[ref.Key]
			if !ok {
				v.Missing = true
				v.Reason = fmt.Sprintf("ConfigMap %s has no key %q", ref.Name, ref.Key)
				if ref.Optional != nil && *ref.Optional {
					v.Reason += "; the reference is optional, so the container started without it"
				}
				break
			}
			v.Value = val
		}

	case e.ValueFrom.SecretKeyRef != nil:
		// Masked by not fetching. This is the whole design: a value kubeside
		// never read cannot leak through a log line, a crash dump, or a bug.
		ref := e.ValueFrom.SecretKeyRef
		v.Source = Source{Kind: SourceSecret, Ref: ref.Name, Key: ref.Key}
		v.Masked = true
		v.CanReveal, v.RevealReason = r.mayReveal(ref.Name)

	case e.ValueFrom.FieldRef != nil:
		path := e.ValueFrom.FieldRef.FieldPath
		v.Source = Source{Kind: SourceDownward, Key: path}
		val, ok := r.downward(path)
		if !ok {
			v.Missing, v.Reason = true, "this field is resolved by the kubelet and is not readable from the pod"
			break
		}
		v.Value = val

	case e.ValueFrom.ResourceFieldRef != nil:
		ref := e.ValueFrom.ResourceFieldRef
		v.Source = Source{Kind: SourceResource, Key: ref.Resource}
		v.Missing = true
		v.Reason = "resolved from the container's resource limits at start; not recoverable from the pod"
	}

	return v
}

// downward reads the pod fields the downward API exposes. Only the ones that
// are genuinely in the object: inventing the rest would be worse than saying
// they are not readable.
func (r *resolver) downward(path string) (string, bool) {
	switch {
	case path == "metadata.name":
		return r.pod.Name, true
	case path == "metadata.namespace":
		return r.pod.Namespace, true
	case path == "metadata.uid":
		return string(r.pod.UID), true
	case path == "spec.nodeName":
		return r.pod.Spec.NodeName, true
	case path == "spec.serviceAccountName":
		return r.pod.Spec.ServiceAccountName, true
	case path == "status.podIP":
		return r.pod.Status.PodIP, true
	case path == "status.hostIP":
		return r.pod.Status.HostIP, true
	case strings.HasPrefix(path, "metadata.labels['"):
		return r.pod.Labels[between(path, "['", "']")], true
	case strings.HasPrefix(path, "metadata.annotations['"):
		return r.pod.Annotations[between(path, "['", "']")], true
	}
	return "", false
}

func (r *resolver) mounts(c corev1.Container) []Mount {
	byName := map[string]corev1.Volume{}
	for _, v := range r.pod.Spec.Volumes {
		byName[v.Name] = v
	}

	var out []Mount
	for _, m := range c.VolumeMounts {
		v, ok := byName[m.Name]
		if !ok {
			continue
		}
		switch {
		case v.ConfigMap != nil:
			out = append(out, Mount{
				Path:   m.MountPath,
				Source: Source{Kind: SourceConfigMap, Ref: v.ConfigMap.Name},
			})
		case v.Secret != nil:
			out = append(out, Mount{
				Path:   m.MountPath,
				Source: Source{Kind: SourceSecret, Ref: v.Secret.SecretName},
				Masked: true,
			})
		}
		if n := len(out); n > 0 && out[n-1].Path == m.MountPath {
			out[n-1].ReadOnly = m.ReadOnly
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// configMap fetches once per name, however many keys reference it.
func (r *resolver) configMap(name string) *configMap {
	if cm, ok := r.maps[name]; ok {
		return cm
	}
	got, err := r.client.CoreV1().ConfigMaps(r.pod.Namespace).Get(r.ctx, name, metav1.GetOptions{})
	cm := &configMap{}
	if err != nil {
		cm.err = err
	} else {
		cm.data = got.Data
	}
	r.maps[name] = cm
	return cm
}

// unreadable phrases the failure as what is missing, not as a status code.
func unreadable(kind, name string, err error) string {
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Sprintf("%s %s does not exist", kind, name)
	case apierrors.IsForbidden(err):
		return fmt.Sprintf("needs read access to %s %s", strings.ToLower(kind), name)
	}
	return fmt.Sprintf("%s %s could not be read: %v", kind, name, err)
}

func between(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

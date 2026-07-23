package clusters

import (
	"context"
	"fmt"
	"sort"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Scope records how much of a cluster kubeside was allowed to read.
//
// A refused cluster-scoped list is a normal answer, not an error: no
// Kubernetes API enumerates the namespaces a user may access, so discovery
// falls back and the UI names the mode it ended up in.
type Scope struct {
	ClusterWide bool
	Namespaces  []string
	// Reason explains a fallback, e.g. a forbidden cluster-scoped list.
	Reason string
}

func (s Scope) String() string {
	if s.ClusterWide {
		return "cluster-wide"
	}
	if len(s.Namespaces) == 0 {
		return "no readable namespace"
	}
	return fmt.Sprintf("namespaces %v", s.Namespaces)
}

// Snapshot is one cluster's app list at a point in time.
type Snapshot struct {
	Context string
	Scope   Scope
	Apps    []apps.App
	// Partial lists kinds that could not be read, so the UI can say what is
	// missing instead of implying the cluster has none of them.
	Partial []string
}

// clientSession adapts a Kubernetes clientset to the Session interface.
type clientSession struct{ client kubernetes.Interface }

func (clientSession) Close() error { return nil }

// KubeConnector is the production Connector. It builds a REST config per
// context, which is where exec credential plugins run.
type KubeConnector struct {
	Opts kubeconfig.Options
	// NewClient is swappable in tests.
	NewClient func(kctx kubeconfig.Context, opts kubeconfig.Options) (kubernetes.Interface, error)
}

func (k KubeConnector) Connect(ctx context.Context, kctx kubeconfig.Context) (Session, error) {
	newClient := k.NewClient
	if newClient == nil {
		newClient = defaultNewClient
	}
	c, err := newClient(kctx, k.Opts)
	if err != nil {
		return nil, err
	}
	// A cheap call proves the connection works, so an expired token or a
	// cluster that is off VPN surfaces at connect time rather than on the
	// first render. The error is returned unwrapped: classify decides whether
	// it is a credential problem or a network one, and wrapping everything as
	// an auth failure would report a refused dial as an expired token.
	if _, err := c.Discovery().ServerVersion(); err != nil {
		return nil, err
	}
	return clientSession{client: c}, nil
}

func defaultNewClient(kctx kubeconfig.Context, opts kubeconfig.Options) (kubernetes.Interface, error) {
	rc, err := kubeconfig.RESTConfigFor(opts, kctx.Name)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(rc)
}

// ClientFor returns the clientset behind a live connection.
func (m *Manager) ClientFor(name string) (kubernetes.Interface, bool) {
	c, ok := m.conn(name)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cs, ok := c.session.(clientSession)
	if !ok {
		return nil, false
	}
	return cs.client, true
}

// Fetch lists workloads for one connected context and groups them.
//
// Errors on individual kinds are recorded rather than fatal: a developer who
// can read Deployments but not CronJobs should still see their Deployments.
func Fetch(ctx context.Context, c kubernetes.Interface, kctx kubeconfig.Context) (Snapshot, error) {
	snap := Snapshot{Context: kctx.Name}
	snap.Scope = discoverScope(ctx, c, kctx)

	targets := snap.Scope.Namespaces
	if snap.Scope.ClusterWide {
		targets = []string{metav1.NamespaceAll}
	}
	if len(targets) == 0 {
		return snap, nil
	}

	var objs []apps.Object
	for _, ns := range targets {
		got, partial := listNamespace(ctx, c, ns)
		objs = append(objs, got...)
		snap.Partial = append(snap.Partial, partial...)
	}

	snap.Partial = dedupe(snap.Partial)
	snap.Apps = apps.Group(objs)
	return snap, nil
}

// discoverScope probes what the user may read.
func discoverScope(ctx context.Context, c kubernetes.Interface, kctx kubeconfig.Context) Scope {
	if _, err := c.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1}); err == nil {
		return Scope{ClusterWide: true}
	} else if !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) {
		// A transport failure is different from a refusal; fall back quietly.
		return fallbackScope(kctx, "namespace list failed")
	}
	return fallbackScope(kctx, "cluster-scoped namespace list forbidden")
}

func fallbackScope(kctx kubeconfig.Context, reason string) Scope {
	ns := kctx.Namespace
	if ns == "" {
		ns = "default"
		reason += "; context sets no namespace, using default"
	}
	return Scope{Namespaces: []string{ns}, Reason: reason}
}

func listNamespace(ctx context.Context, c kubernetes.Interface, ns string) ([]apps.Object, []string) {
	var out []apps.Object
	var partial []string
	opts := metav1.ListOptions{}

	if l, err := c.AppsV1().Deployments(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("Deployment", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "Deployment")
	}

	if l, err := c.AppsV1().StatefulSets(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("StatefulSet", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "StatefulSet")
	}

	if l, err := c.AppsV1().DaemonSets(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("DaemonSet", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "DaemonSet")
	}

	if l, err := c.BatchV1().CronJobs(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("CronJob", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "CronJob")
	}

	if l, err := c.BatchV1().Jobs(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("Job", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "Job")
	}

	if l, err := c.AppsV1().ReplicaSets(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("ReplicaSet", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "ReplicaSet")
	}

	if l, err := c.CoreV1().Pods(ns).List(ctx, opts); err == nil {
		for i := range l.Items {
			out = append(out, fromMeta("Pod", &l.Items[i].ObjectMeta))
		}
	} else {
		partial = append(partial, "Pod")
	}

	return out, partial
}

// fromMeta adapts Kubernetes metadata to the grouping engine's input type,
// keeping the engine free of client-go types and trivially testable.
func fromMeta(kind string, m *metav1.ObjectMeta) apps.Object {
	o := apps.Object{
		Kind:        kind,
		Name:        m.Name,
		Namespace:   m.Namespace,
		UID:         string(m.UID),
		Labels:      m.Labels,
		Annotations: m.Annotations,
	}
	for _, ref := range m.OwnerReferences {
		o.Owners = append(o.Owners, apps.Owner{
			Kind:       ref.Kind,
			Name:       ref.Name,
			UID:        string(ref.UID),
			Controller: ref.Controller != nil && *ref.Controller,
		})
	}
	return o
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Unused imports guard: these types are referenced by the adapters above.
var (
	_ = appsv1.Deployment{}
	_ = batchv1.CronJob{}
	_ = corev1.Pod{}
)

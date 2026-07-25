package logs

import (
	"context"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DefaultTail is how much scrollback a stream opens with. Enough to see what
// just happened, short enough that opening a week-old pod does not pull the
// kubelet's entire retained buffer across the wire.
const DefaultTail int64 = 2000

// KubeSource reads logs from a cluster.
//
// The pod set comes from a caller-supplied function rather than a label
// selector, because the grouping engine already decides which pods belong to an
// app, and a selector would be a second, disagreeing answer.
type KubeSource struct {
	Client    kubernetes.Interface
	Namespace string
	PodNames  func(ctx context.Context) ([]string, error)
}

// Targets lists every container of every pod currently backing the workload.
func (k KubeSource) Targets(ctx context.Context) ([]Target, error) {
	names, err := k.PodNames(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	if len(want) == 0 {
		return nil, nil
	}

	// One list beats one get per pod: a deployment scaled to fifty replicas
	// should cost one request per refresh, not fifty.
	list, err := k.Client.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var out []Target
	for i := range list.Items {
		p := &list.Items[i]
		if !want[p.Name] {
			continue
		}
		for _, c := range p.Spec.InitContainers {
			out = append(out, Target{Namespace: k.Namespace, Pod: p.Name, Container: c.Name, Init: true})
		}
		for _, c := range p.Spec.Containers {
			out = append(out, Target{Namespace: k.Namespace, Pod: p.Name, Container: c.Name})
		}
	}
	return out, nil
}

// Open starts one container's stream. Timestamps are always requested: without
// them there is no way to merge replicas into a single true order.
func (k KubeSource) Open(ctx context.Context, t Target, opts OpenOptions) (io.ReadCloser, error) {
	tail := opts.TailLines
	if tail <= 0 {
		tail = DefaultTail
	}
	o := &corev1.PodLogOptions{
		Container:  t.Container,
		Follow:     opts.Follow,
		Previous:   opts.Previous,
		Timestamps: true,
		TailLines:  &tail,
	}
	if opts.SinceTime != nil {
		st := metav1.NewTime(*opts.SinceTime)
		o.SinceTime = &st
	}
	return k.Client.CoreV1().Pods(k.Namespace).GetLogs(t.Pod, o).Stream(ctx)
}

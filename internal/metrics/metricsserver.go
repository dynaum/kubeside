package metrics

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// PodMetricsAPI is the slice of the metrics API this package uses.
//
// Narrowing the dependency to one method keeps the package testable with a
// hand-written fake. The generated metrics fake clientset does not track
// PodMetrics correctly in v0.36, so depending on the full clientset would mean
// the read path could only be exercised against a live cluster.
type PodMetricsAPI interface {
	ListPodMetrics(ctx context.Context, namespace string) (*metricsapi.PodMetricsList, error)
}

// clientsetAPI adapts the real generated clientset to PodMetricsAPI.
type clientsetAPI struct{ c metricsv.Interface }

func (a clientsetAPI) ListPodMetrics(ctx context.Context, ns string) (*metricsapi.PodMetricsList, error) {
	return a.c.MetricsV1beta1().PodMetricses(ns).List(ctx, metav1.ListOptions{})
}

// FromClientset wraps a generated metrics clientset for production use.
func FromClientset(c metricsv.Interface) PodMetricsAPI {
	if c == nil {
		return nil
	}
	return clientsetAPI{c: c}
}

// metricsServerGroup is the aggregated API metrics-server registers.
const metricsServerGroup = "metrics.k8s.io"

// MetricsServer reads from the metrics.k8s.io aggregated API.
//
// This is the default source because it is the one most clusters actually
// have, and because assuming Prometheus is the mistake that produces wrong
// numbers everywhere else in this ecosystem.
type MetricsServer struct {
	client    PodMetricsAPI
	available bool
	reason    string
}

// NewMetricsServer probes for the aggregated API and returns a source that
// reports honestly either way.
//
// A probe failure is not an error: a cluster without metrics-server is normal,
// and the caller wants a source that says so rather than an error to handle.
func NewMetricsServer(d discovery.DiscoveryInterface, c PodMetricsAPI) *MetricsServer {
	ms := &MetricsServer{client: c}

	groups, err := d.ServerGroups()
	if err != nil {
		ms.reason = fmt.Sprintf("could not probe for %s: %v", metricsServerGroup, err)
		return ms
	}
	for _, g := range groups.Groups {
		if g.Name == metricsServerGroup {
			ms.available = c != nil
			if c == nil {
				ms.reason = "metrics.k8s.io is registered but no client was supplied"
			}
			return ms
		}
	}
	ms.reason = "metrics-server is not installed (metrics.k8s.io is not registered)"
	return ms
}

func (m *MetricsServer) Name() string { return "metrics-server" }

func (m *MetricsServer) Available() bool { return m != nil && m.available }

func (m *MetricsServer) Unavailable() string {
	if m == nil {
		return "no metrics source"
	}
	if m.reason != "" {
		return m.reason
	}
	if !m.available {
		return "metrics-server is unavailable"
	}
	return ""
}

// PodSamples returns one sample per container.
//
// Aggregation to pod level is deliberately left to ByPod. Returning
// per-container readings keeps the raw shape intact, which is what the
// resolved-config and pod views need later, and confines the summing that
// competitors get wrong to a single tested function.
func (m *MetricsServer) PodSamples(ctx context.Context, namespace string) ([]Sample, error) {
	if !m.Available() {
		return nil, nil
	}

	list, err := m.client.ListPodMetrics(ctx, namespace)
	if err != nil {
		// A read failure must not be reported as zero usage. Returning the
		// error lets the caller fall back to showing nothing.
		return nil, fmt.Errorf("read pod metrics: %w", err)
	}

	out := make([]Sample, 0, len(list.Items))
	for i := range list.Items {
		pm := &list.Items[i]
		for _, c := range pm.Containers {
			s := Sample{
				Namespace: pm.Namespace,
				Pod:       pm.Name,
				Container: c.Name,
				Source:    m.Name(),
				Timestamp: pm.Timestamp.Time,
				Window:    time.Duration(pm.Window.Duration),
			}
			if cpu := c.Usage.Cpu(); cpu != nil {
				s.CPUMilli = cpu.MilliValue()
			}
			if mem := c.Usage.Memory(); mem != nil {
				s.MemoryBytes = mem.Value()
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// Probe picks a source for a cluster.
//
// Order matters and is documented in docs/05-architecture.md: the aggregated
// API first because it is both the most common and the cheapest to verify,
// then a configured Prometheus, then none. The result is always a usable
// Source, never nil.
func Probe(d discovery.DiscoveryInterface, c PodMetricsAPI, prometheusEndpoint string) Source {
	if ms := NewMetricsServer(d, c); ms.Available() {
		return ms
	} else if prometheusEndpoint == "" {
		return None{Reason: ms.Unavailable()}
	}
	return Prometheus{Endpoint: prometheusEndpoint}
}

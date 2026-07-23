// Package metrics reads CPU and memory usage from whatever source a cluster
// actually has.
//
// This exists because of the loudest complaint class in the ecosystem. The
// Lens lineage hardcodes Prometheus query shapes, so pointing those tools at
// metrics-server or VictoriaMetrics makes values double, zero out, or vanish
// (Freelens #466, #627, #524, #1670, #964, #1111, #1555, #1883). The design
// consequence recorded in docs/01-problem.md is that a source is an interface
// with metrics-server as the default, and that a value which might be wrong is
// not displayed at all.
//
// Two rules the rest of the product depends on:
//
//   - Never a zero, never a guess. An unavailable source yields no samples, so
//     the UI hides the column rather than rendering 0 and implying idleness.
//   - Every sample names its source, so a suspicious number is traceable to
//     the thing that produced it instead of being mysterious.
package metrics

import (
	"context"
	"fmt"
	"time"
)

// Sample is one container's usage at a point in time.
type Sample struct {
	Namespace string
	Pod       string
	Container string

	// CPUMilli is millicores. MemoryBytes is bytes. Both are raw readings; no
	// unit conversion or rounding happens before the UI, which formats them.
	CPUMilli    int64
	MemoryBytes int64

	// Source names the implementation that produced this reading. It reaches
	// the UI so a doubled or missing value is attributable.
	Source string
	// Timestamp and Window describe when the reading was taken and over what
	// interval. A stale window is worth surfacing rather than hiding.
	Timestamp time.Time
	Window    time.Duration
}

// Source reads usage for pods in a namespace.
type Source interface {
	// Name identifies the implementation, e.g. "metrics-server".
	Name() string
	// Available reports whether this source can answer queries right now.
	Available() bool
	// Unavailable explains why not, phrased so the UI can render it as an
	// empty state naming what to install.
	Unavailable() string
	// PodSamples returns usage for pods in a namespace. Passing an empty
	// namespace means all namespaces the caller may read.
	PodSamples(ctx context.Context, namespace string) ([]Sample, error)
}

// None is the source used when a cluster has no metrics at all.
//
// It is a real implementation rather than a nil check, so every call site
// handles "no metrics" the same way and nobody has to remember a guard.
type None struct {
	// Reason explains what was probed and what was missing.
	Reason string
}

func (None) Name() string { return "none" }

func (None) Available() bool { return false }

func (n None) Unavailable() string {
	if n.Reason != "" {
		return n.Reason
	}
	return "no metrics source is configured on this cluster"
}

func (None) PodSamples(context.Context, string) ([]Sample, error) { return nil, nil }

// Prometheus is a placeholder for the source that ships after v1.
//
// It exists now so the probe chain and the UI's empty state are exercised by
// the real interface rather than by a special case added later. It never
// claims availability, so it can never contribute a wrong number.
type Prometheus struct {
	Endpoint string
}

func (Prometheus) Name() string { return "prometheus" }

func (Prometheus) Available() bool { return false }

func (p Prometheus) Unavailable() string {
	if p.Endpoint == "" {
		return "no Prometheus endpoint is configured"
	}
	return fmt.Sprintf("the Prometheus source is not implemented yet (configured endpoint %s)", p.Endpoint)
}

func (Prometheus) PodSamples(context.Context, string) ([]Sample, error) { return nil, nil }

// ByPod folds container samples into one reading per pod.
//
// Summing containers is the correct aggregation and also the thing Freelens
// gets wrong: its pod detail double-counts, which is what #964 and #1111
// report. Doing it in one place, with a test, keeps that defect out.
func ByPod(samples []Sample) map[string]Sample {
	out := map[string]Sample{}
	for _, s := range samples {
		key := s.Namespace + "/" + s.Pod
		agg, ok := out[key]
		if !ok {
			agg = Sample{
				Namespace: s.Namespace,
				Pod:       s.Pod,
				Source:    s.Source,
				Timestamp: s.Timestamp,
				Window:    s.Window,
			}
		}
		agg.CPUMilli += s.CPUMilli
		agg.MemoryBytes += s.MemoryBytes
		// Report the oldest reading in the set, so a pod is never described as
		// fresher than its stalest container.
		if !s.Timestamp.IsZero() && (agg.Timestamp.IsZero() || s.Timestamp.Before(agg.Timestamp)) {
			agg.Timestamp = s.Timestamp
		}
		out[key] = agg
	}
	return out
}

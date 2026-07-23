package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// fakeAPI stands in for the metrics API. The generated fake clientset does not
// track PodMetrics correctly in v0.36, so this package owns a narrow interface
// and fakes that instead of depending on a broken generator.
type fakeAPI struct {
	items []metricsapi.PodMetrics
	err   error
}

func (f fakeAPI) ListPodMetrics(_ context.Context, ns string) (*metricsapi.PodMetricsList, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &metricsapi.PodMetricsList{}
	for _, it := range f.items {
		if ns == "" || it.Namespace == ns {
			out.Items = append(out.Items, it)
		}
	}
	return out, nil
}

// fakeDiscovery reports whichever API groups a test wants.
type fakeDiscovery struct {
	discovery.DiscoveryInterface
	groups []string
	err    error
}

func (f fakeDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &metav1.APIGroupList{}
	for _, g := range f.groups {
		out.Groups = append(out.Groups, metav1.APIGroup{Name: g})
	}
	return out, nil
}

func (fakeDiscovery) ServerVersion() (*version.Info, error) { return &version.Info{}, nil }

func podMetrics(ns, pod string, ts time.Time, containers map[string][2]string) *metricsapi.PodMetrics {
	pm := &metricsapi.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: ns},
		Timestamp:  metav1.NewTime(ts),
		Window:     metav1.Duration{Duration: 30 * time.Second},
	}
	names := make([]string, 0, len(containers))
	for n := range containers {
		names = append(names, n)
	}
	// Deterministic order.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, n := range names {
		v := containers[n]
		pm.Containers = append(pm.Containers, metricsapi.ContainerMetrics{
			Name: n,
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(v[0]),
				corev1.ResourceMemory: resource.MustParse(v[1]),
			},
		})
	}
	return pm
}

func TestProbePicksMetricsServerWhenRegistered(t *testing.T) {
	d := fakeDiscovery{groups: []string{"apps", metricsServerGroup}}
	src := Probe(d, fakeAPI{}, "")
	if !src.Available() {
		t.Fatalf("source %s unavailable: %s", src.Name(), src.Unavailable())
	}
	if src.Name() != "metrics-server" {
		t.Fatalf("source = %q, want metrics-server as the default", src.Name())
	}
}

// A cluster with no metrics is normal, not an error. The source must say so.
func TestProbeFallsBackToNoneAndExplainsWhy(t *testing.T) {
	d := fakeDiscovery{groups: []string{"apps"}}
	src := Probe(d, fakeAPI{}, "")
	if src.Available() {
		t.Fatal("want unavailable when metrics.k8s.io is absent")
	}
	if src.Name() != "none" {
		t.Fatalf("source = %q, want none", src.Name())
	}
	if !strings.Contains(src.Unavailable(), "metrics-server is not installed") {
		t.Fatalf("reason = %q, should name what to install", src.Unavailable())
	}
}

func TestProbeUsesPrometheusOnlyWhenConfiguredAndMetricsServerAbsent(t *testing.T) {
	d := fakeDiscovery{groups: []string{"apps"}}
	src := Probe(d, fakeAPI{}, "http://prom:9090")
	if src.Name() != "prometheus" {
		t.Fatalf("source = %q, want prometheus", src.Name())
	}
	// Not implemented yet, so it must never claim availability and can never
	// contribute a wrong number.
	if src.Available() {
		t.Fatal("the Prometheus source is not implemented and must not claim availability")
	}
}

func TestProbePrefersMetricsServerOverPrometheus(t *testing.T) {
	d := fakeDiscovery{groups: []string{metricsServerGroup}}
	src := Probe(d, fakeAPI{}, "http://prom:9090")
	if src.Name() != "metrics-server" {
		t.Fatalf("source = %q, want metrics-server to win the probe order", src.Name())
	}
}

func TestProbeSurvivesDiscoveryFailure(t *testing.T) {
	d := fakeDiscovery{err: errors.New("apiserver unreachable")}
	src := Probe(d, fakeAPI{}, "")
	if src.Available() {
		t.Fatal("a failed probe must not claim availability")
	}
	if !strings.Contains(src.Unavailable(), "could not probe") {
		t.Fatalf("reason = %q, should say the probe failed rather than blaming the install", src.Unavailable())
	}
}

func TestSamplesCarryTheirSourceAndWindow(t *testing.T) {
	ts := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	c := fakeAPI{items: []metricsapi.PodMetrics{*podMetrics("ns", "app-a", ts, map[string][2]string{"app": {"250m", "128Mi"}})}}
	src := Probe(fakeDiscovery{groups: []string{metricsServerGroup}}, c, "")

	got, err := src.PodSamples(context.Background(), "ns")
	if err != nil {
		t.Fatalf("PodSamples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d samples, want 1", len(got))
	}
	s := got[0]
	if s.Source != "metrics-server" {
		t.Errorf("source = %q; an untraceable number is the defect this guards against", s.Source)
	}
	if s.CPUMilli != 250 {
		t.Errorf("cpu = %d, want 250", s.CPUMilli)
	}
	if s.MemoryBytes != 128*1024*1024 {
		t.Errorf("memory = %d, want 128Mi in bytes", s.MemoryBytes)
	}
	if s.Window != 30*time.Second {
		t.Errorf("window = %s, want 30s", s.Window)
	}
	if !s.Timestamp.Equal(ts) {
		t.Errorf("timestamp = %s, want %s", s.Timestamp, ts)
	}
}

// The defect Freelens #964 and #1111 report: a pod's usage shown as double
// the container running inside it. Summing happens once, here, with a test.
func TestByPodSumsContainersExactlyOnce(t *testing.T) {
	ts := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	samples := []Sample{
		{Namespace: "ns", Pod: "p", Container: "app", CPUMilli: 250, MemoryBytes: 100, Source: "metrics-server", Timestamp: ts},
		{Namespace: "ns", Pod: "p", Container: "istio-proxy", CPUMilli: 50, MemoryBytes: 40, Source: "metrics-server", Timestamp: ts},
	}
	got := ByPod(samples)
	if len(got) != 1 {
		t.Fatalf("got %d pods, want 1", len(got))
	}
	p := got["ns/p"]
	if p.CPUMilli != 300 {
		t.Errorf("cpu = %d, want 300 (250+50), not doubled", p.CPUMilli)
	}
	if p.MemoryBytes != 140 {
		t.Errorf("memory = %d, want 140 (100+40)", p.MemoryBytes)
	}
}

func TestByPodReportsTheStalestReading(t *testing.T) {
	older := time.Date(2026, 7, 23, 13, 59, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	got := ByPod([]Sample{
		{Namespace: "ns", Pod: "p", Container: "a", Timestamp: newer},
		{Namespace: "ns", Pod: "p", Container: "b", Timestamp: older},
	})
	if !got["ns/p"].Timestamp.Equal(older) {
		t.Fatalf("timestamp = %s, want the oldest: a pod is never fresher than its stalest container",
			got["ns/p"].Timestamp)
	}
}

func TestByPodSeparatesPodsAcrossNamespaces(t *testing.T) {
	got := ByPod([]Sample{
		{Namespace: "a", Pod: "same", CPUMilli: 1},
		{Namespace: "b", Pod: "same", CPUMilli: 2},
	})
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: identical pod names in different namespaces are different pods", len(got))
	}
}

// An unavailable source must yield nothing, so the UI hides the column rather
// than rendering 0 and implying the workload is idle.
func TestUnavailableSourcesReturnNoSamplesNotZeroes(t *testing.T) {
	for _, src := range []Source{
		None{},
		Prometheus{Endpoint: "http://prom:9090"},
		&MetricsServer{},
	} {
		got, err := src.PodSamples(context.Background(), "ns")
		if err != nil {
			t.Errorf("%s: %v", src.Name(), err)
		}
		if len(got) != 0 {
			t.Errorf("%s returned %d samples while unavailable; a zero would read as idle", src.Name(), len(got))
		}
		if src.Unavailable() == "" {
			t.Errorf("%s is unavailable but does not explain why", src.Name())
		}
	}
}

// A read failure is not zero usage. It must surface as an error so the caller
// can show nothing rather than a fabricated reading.
func TestReadFailureIsAnErrorNotZeroUsage(t *testing.T) {
	c := fakeAPI{err: errors.New("metrics API timed out")}
	src := Probe(fakeDiscovery{groups: []string{metricsServerGroup}}, c, "")

	got, err := src.PodSamples(context.Background(), "ns")
	if err == nil {
		t.Fatal("want an error; silently returning no samples would look like idle workloads")
	}
	if len(got) != 0 {
		t.Errorf("got %d samples alongside an error", len(got))
	}
}

func TestSourceNamesAreStable(t *testing.T) {
	for src, want := range map[Source]string{
		None{}:           "none",
		Prometheus{}:     "prometheus",
		&MetricsServer{}: "metrics-server",
	} {
		if got := src.Name(); got != want {
			t.Errorf("name = %q, want %q", got, want)
		}
	}
}

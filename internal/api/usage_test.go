package api

import (
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/metrics"
)

func usagePod(name string) apps.Object {
	return apps.Object{
		Kind: "Pod", Name: name, Namespace: "team-a", Created: time.Now().Add(-time.Hour),
		Status: &apps.Status{Phase: "Running", Ready: true},
	}
}

func checkoutApp(pods ...apps.Object) apps.App {
	return apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: append([]apps.Object{
			deployment("checkout", time.Now().Add(-time.Hour), &apps.Status{
				DesiredReplicas: int32(len(pods)), ReadyReplicas: int32(len(pods)), Image: "checkout:1.4.2"}),
		}, pods...),
	}
}

func sample(ns, pod string, cpu, mem int64) metrics.Sample {
	return metrics.Sample{Namespace: ns, Pod: pod, CPUMilli: cpu, MemoryBytes: mem, Source: "metrics-server"}
}

func usageRow(t *testing.T, a apps.App, usage map[string]metrics.Sample) AppView {
	t.Helper()
	got := AppsFromSnapshot(rowSnap(a), "live",
		MetricsInfo{Source: "metrics-server", Available: true}, usage)
	if len(got.Apps) != 1 {
		t.Fatalf("apps = %d, want 1", len(got.Apps))
	}
	return got.Apps[0]
}

// An app's usage is the sum of its replicas. That is the number a developer
// compares against the limit they set on the workload.
func TestUsageSumsTheAppsPods(t *testing.T) {
	a := checkoutApp(usagePod("checkout-1"), usagePod("checkout-2"))
	usage := map[string]metrics.Sample{
		"team-a/checkout-1": sample("team-a", "checkout-1", 120, 256<<20),
		"team-a/checkout-2": sample("team-a", "checkout-2", 80, 128<<20),
		// A pod belonging to a different app in the same namespace.
		"team-a/payments-9": sample("team-a", "payments-9", 900, 900<<20),
	}

	got := usageRow(t, a, usage)
	if got.CPUMilli != 200 {
		t.Errorf("cpuMilli = %d, want 200", got.CPUMilli)
	}
	if got.MemoryBytes != 384<<20 {
		t.Errorf("memoryBytes = %d, want 384Mi", got.MemoryBytes)
	}
	if got.Measured != 2 {
		t.Errorf("measured = %d, want 2", got.Measured)
	}
}

// A pod the source has not reported yet is not a pod using nothing. Measured
// says how many readings the total is built from, so the UI can mark a partial
// answer instead of showing it as complete.
func TestUsageCountsOnlyPodsThatReported(t *testing.T) {
	a := checkoutApp(usagePod("checkout-1"), usagePod("checkout-2"))
	usage := map[string]metrics.Sample{
		"team-a/checkout-1": sample("team-a", "checkout-1", 120, 256<<20),
	}

	got := usageRow(t, a, usage)
	if got.Measured != 1 {
		t.Errorf("measured = %d, want 1", got.Measured)
	}
	if got.Pods != 2 {
		t.Errorf("pods = %d, want 2", got.Pods)
	}
	if got.CPUMilli != 120 {
		t.Errorf("cpuMilli = %d, want only the pod that reported", got.CPUMilli)
	}
}

// No source, no readings, no columns. Zero usage would say the app is idle.
func TestUsageIsAbsentWithoutASource(t *testing.T) {
	a := checkoutApp(usagePod("checkout-1"))

	got := AppsFromSnapshot(rowSnap(a), "live",
		MetricsInfo{Source: "none", Available: false, Reason: "metrics-server is not installed"}, nil)
	if got.Apps[0].Measured != 0 {
		t.Errorf("measured = %d, want 0", got.Apps[0].Measured)
	}
	if got.Apps[0].CPUMilli != 0 || got.Apps[0].MemoryBytes != 0 {
		t.Errorf("usage = %d/%d, want nothing", got.Apps[0].CPUMilli, got.Apps[0].MemoryBytes)
	}
}

// A genuinely idle pod reads zero, and that is a reading. It has to be
// distinguishable from a pod nobody measured, which is what Measured is for.
func TestUsageDistinguishesAMeasuredZero(t *testing.T) {
	a := checkoutApp(usagePod("checkout-1"))
	usage := map[string]metrics.Sample{
		"team-a/checkout-1": sample("team-a", "checkout-1", 0, 0),
	}

	got := usageRow(t, a, usage)
	if got.Measured != 1 {
		t.Errorf("measured = %d, want 1: a zero reading was taken", got.Measured)
	}
}

package api

import (
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
)

func rowSnap(list ...apps.App) clusters.Snapshot {
	return clusters.Snapshot{Context: "qa1", Apps: list}
}

func rowOf(t *testing.T, a apps.App) AppView {
	t.Helper()
	got := AppsFromSnapshot(rowSnap(a), "live", MetricsInfo{})
	if len(got.Apps) != 1 {
		t.Fatalf("apps = %d, want 1", len(got.Apps))
	}
	return got.Apps[0]
}

func deployment(name string, created time.Time, st *apps.Status) apps.Object {
	return apps.Object{Kind: "Deployment", Name: name, Namespace: "team-a", Created: created, Status: st}
}

// The running version is the first thing asked of a row. Reading it should not
// cost a click into the detail view.
func TestRowCarriesTheImageTag(t *testing.T) {
	now := time.Now()
	a := apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: []apps.Object{
			deployment("checkout", now.Add(-30*24*time.Hour), &apps.Status{
				DesiredReplicas: 2, ReadyReplicas: 2, Image: "ghcr.io/acme/checkout:1.4.2"}),
		},
	}

	got := rowOf(t, a)
	if got.Tag != "1.4.2" {
		t.Errorf("tag = %q, want the tag alone", got.Tag)
	}
	if got.Image != "ghcr.io/acme/checkout:1.4.2" {
		t.Errorf("image = %q, want the full reference for the tooltip", got.Image)
	}
}

// A pod may still be running the previous revision while a rollout is in
// flight. The spec is what was asked for, so the spec wins.
func TestRowPrefersTheWorkloadImageOverAPodsDuringARollout(t *testing.T) {
	now := time.Now()
	a := apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: []apps.Object{
			{Kind: "Pod", Name: "checkout-old", Created: now.Add(-time.Hour),
				Status: &apps.Status{Phase: "Running", Ready: true, Image: "checkout:1.4.1"}},
			deployment("checkout", now.Add(-30*24*time.Hour), &apps.Status{
				DesiredReplicas: 2, ReadyReplicas: 1, Image: "checkout:1.4.2"}),
		},
	}

	if got := rowOf(t, a).Tag; got != "1.4.2" {
		t.Errorf("tag = %q, want the workload spec's tag", got)
	}
}

// The revision rolled when the newest object appeared. The Deployment is older
// than every revision it has produced, so its own age answers a different
// question than the one the column asks.
func TestRowRevisionTimeComesFromTheNewestObject(t *testing.T) {
	rolled := time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Second)
	a := apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: []apps.Object{
			deployment("checkout", rolled.Add(-30*24*time.Hour), &apps.Status{
				DesiredReplicas: 1, ReadyReplicas: 1, Image: "checkout:1.4.2"}),
			{Kind: "ReplicaSet", Name: "checkout-8f7", Created: rolled},
			{Kind: "Pod", Name: "checkout-8f7-abc", Created: rolled,
				Status: &apps.Status{Phase: "Running", Ready: true}},
		},
	}

	want := rolled.Format(time.RFC3339)
	if got := rowOf(t, a).RevisionAt; got != want {
		t.Errorf("revisionAt = %q, want %q", got, want)
	}
}

// One flapping replica among four is invisible in a ready ratio of 4/4.
func TestRowSumsRestartsAcrossReplicas(t *testing.T) {
	now := time.Now()
	a := apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: []apps.Object{
			deployment("checkout", now.Add(-time.Hour), &apps.Status{
				DesiredReplicas: 2, ReadyReplicas: 2, Image: "checkout:1.4.2"}),
			{Kind: "Pod", Name: "checkout-1", Created: now, Status: &apps.Status{Phase: "Running", Ready: true, RestartCount: 6}},
			{Kind: "Pod", Name: "checkout-2", Created: now, Status: &apps.Status{Phase: "Running", Ready: true, RestartCount: 1}},
		},
	}

	got := rowOf(t, a)
	if got.Restarts != 7 {
		t.Errorf("restarts = %d, want 7", got.Restarts)
	}
	if got.Pods != 2 {
		t.Errorf("pods = %d, want 2", got.Pods)
	}
}

// Zero pods read means zero restarts observed, which is not the same as an app
// that has never restarted. Pods is what lets the row render a dash instead of
// a zero nobody measured.
func TestRowWithNoPodsReportsNoPods(t *testing.T) {
	a := apps.App{
		Key: apps.Key{Namespace: "team-b", Name: "billing"}, Kind: "CronJob",
		Workloads: []apps.Object{
			{Kind: "CronJob", Name: "billing", Created: time.Now().Add(-time.Hour)},
		},
	}

	got := rowOf(t, a)
	if got.Pods != 0 {
		t.Errorf("pods = %d, want 0", got.Pods)
	}
	if got.Restarts != 0 {
		t.Errorf("restarts = %d, want 0", got.Restarts)
	}
}

// A metadata-only read knows no image. An empty tag renders as an em dash; a
// guessed one would be a claim about which version is live.
func TestRowWithoutAnImageClaimsNoTag(t *testing.T) {
	a := apps.App{
		Key: apps.Key{Namespace: "team-b", Name: "billing"}, Kind: "CronJob",
		Workloads: []apps.Object{
			{Kind: "CronJob", Name: "billing", Created: time.Now().Add(-time.Hour)},
		},
	}

	got := rowOf(t, a)
	if got.Tag != "" || got.Image != "" {
		t.Errorf("tag = %q, image = %q, want both empty", got.Tag, got.Image)
	}
}

// An object with no creation stamp yields no revision time rather than the zero
// time, which would render as an app that rolled in the year one.
func TestRowWithoutACreationStampHasNoRevisionTime(t *testing.T) {
	a := apps.App{
		Key: apps.Key{Namespace: "team-b", Name: "search"}, Kind: "Deployment",
		Workloads: []apps.Object{
			{Kind: "Deployment", Name: "search", Status: &apps.Status{DesiredReplicas: 1}},
		},
	}

	if got := rowOf(t, a).RevisionAt; got != "" {
		t.Errorf("revisionAt = %q, want empty", got)
	}
}

// diffApps compares rows by value, so a field derived from the wall clock would
// mark every row changed on every re-read and send the whole list every five
// seconds. The revision goes on the wire as the moment it happened; the browser
// turns it into an age.
func TestUnchangedClusterProducesNoPatchAsTimePasses(t *testing.T) {
	a := apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: []apps.Object{
			deployment("checkout", time.Now().Add(-2*time.Hour), &apps.Status{
				DesiredReplicas: 2, ReadyReplicas: 2, Image: "checkout:1.4.2"}),
			{Kind: "Pod", Name: "checkout-1", Created: time.Now().Add(-2 * time.Hour),
				Status: &apps.Status{Phase: "Running", Ready: true}},
		},
	}

	first := AppsFromSnapshot(rowSnap(a), "live", MetricsInfo{})
	second := AppsFromSnapshot(rowSnap(a), "live", MetricsInfo{})

	if _, changed := diffApps(first, second); changed {
		t.Fatal("an unchanged cluster produced a patch; a row field is following the clock")
	}
}

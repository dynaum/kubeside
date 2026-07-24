package api

import (
	"encoding/json"
	"testing"
)

func app(ns, name, health string) AppView {
	return AppView{Namespace: ns, Name: name, Kind: "Deployment", Health: health, Ready: "1/1", Objects: 1}
}

func view(apps ...AppView) AppsView {
	return AppsView{
		Context: "qa",
		State:   "live",
		Scope:   "cluster-wide",
		Apps:    apps,
		Metrics: MetricsInfo{Source: "metrics-server", Available: true},
	}
}

func TestDiffOfIdenticalViewsIsEmpty(t *testing.T) {
	v := view(app("team-a", "checkout", "healthy"))
	patch, changed := diffApps(v, v)
	if changed {
		t.Fatalf("identical views produced a patch: %+v", patch)
	}
}

func TestDiffDetectsAnAddedApp(t *testing.T) {
	prev := view(app("team-a", "checkout", "healthy"))
	next := view(app("team-a", "checkout", "healthy"), app("team-b", "search", "healthy"))

	patch, changed := diffApps(prev, next)
	if !changed {
		t.Fatal("a new app produced no patch")
	}
	if len(patch.Added) != 1 || patch.Added[0].Name != "search" {
		t.Fatalf("added = %+v, want the one new app", patch.Added)
	}
	if len(patch.Changed) != 0 || len(patch.Removed) != 0 {
		t.Errorf("patch touched rows it should not: %+v", patch)
	}
}

func TestDiffDetectsARemovedApp(t *testing.T) {
	prev := view(app("team-a", "checkout", "healthy"), app("team-b", "search", "healthy"))
	next := view(app("team-a", "checkout", "healthy"))

	patch, changed := diffApps(prev, next)
	if !changed {
		t.Fatal("a deleted app produced no patch")
	}
	if len(patch.Removed) != 1 || patch.Removed[0] != "team-b/search" {
		t.Fatalf("removed = %v, want [team-b/search]", patch.Removed)
	}
}

// The common case: a rollout flips one row from healthy to progressing. Only
// that row travels, which is the whole point of the delta path.
func TestDiffDetectsAChangedApp(t *testing.T) {
	prev := view(app("team-a", "checkout", "healthy"), app("team-b", "search", "healthy"))
	next := view(app("team-a", "checkout", "progressing"), app("team-b", "search", "healthy"))

	patch, changed := diffApps(prev, next)
	if !changed {
		t.Fatal("a health change produced no patch")
	}
	if len(patch.Changed) != 1 || patch.Changed[0].Health != "progressing" {
		t.Fatalf("changed = %+v, want checkout progressing", patch.Changed)
	}
	if len(patch.Added) != 0 || len(patch.Removed) != 0 {
		t.Errorf("patch touched rows it should not: %+v", patch)
	}
}

// View-level facts matter as much as rows: a cluster going stale, or a
// namespace becoming unreadable, changes what the screen is allowed to claim.
func TestDiffCarriesViewMetadataWhenItChanges(t *testing.T) {
	prev := view(app("team-a", "checkout", "healthy"))
	next := prev
	next.State = "stale"
	next.Partial = []string{"CronJob"}

	patch, changed := diffApps(prev, next)
	if !changed {
		t.Fatal("a state change produced no patch")
	}
	if patch.Meta == nil {
		t.Fatal("meta is nil; the UI would keep claiming the data is live")
	}
	if patch.Meta.State != "stale" || len(patch.Meta.Partial) != 1 {
		t.Errorf("meta = %+v", patch.Meta)
	}
}

func TestDiffOmitsMetadataWhenOnlyRowsChange(t *testing.T) {
	prev := view(app("team-a", "checkout", "healthy"))
	next := view(app("team-a", "checkout", "failed"))

	patch, _ := diffApps(prev, next)
	if patch.Meta != nil {
		t.Errorf("meta = %+v, want nil when only a row changed", patch.Meta)
	}
}

// An empty patch must never serialize into something the client mistakes for
// "every app was deleted".
func TestPatchJSONOmitsEmptyFields(t *testing.T) {
	b, err := json.Marshal(AppsPatch{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("empty patch = %s, want {}", b)
	}
}

func TestAppKeyIsNamespaceAndName(t *testing.T) {
	if got := appKey(app("team-a", "checkout", "healthy")); got != "team-a/checkout" {
		t.Errorf("appKey = %q", got)
	}
}

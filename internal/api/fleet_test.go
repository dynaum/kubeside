package api

import (
	"strings"
	"testing"

	"github.com/dynaum/kubeside/internal/fleet"
)

// TestFleetAsksEveryContext proves the basic shape: one row per cluster, the
// newest tag named, and every cluster behind it counted.
func TestFleetAsksEveryContext(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-us-east": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.13.1"}}},
		"prod-eu-west": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.12.0"}}},
	})

	v := s.Fleet("checkout", "shop")

	if v.Clusters != 3 {
		t.Fatalf("clusters = %d, want 3", v.Clusters)
	}
	if v.Newest != "v2.14.0" {
		t.Errorf("newest = %q, want v2.14.0", v.Newest)
	}
	if v.Behind != 2 {
		t.Errorf("behind = %d, want 2", v.Behind)
	}
}

// TestFleetNamesAnUnreachableClusterRatherThanOmittingIt proves a cluster that
// never answered still gets a row: dropping it would read as "not deployed
// there", which is a different fact from "we could not ask".
func TestFleetNamesAnUnreachableClusterRatherThanOmittingIt(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-eu-west": {env: "prod", unreachable: true},
	})

	v := s.Fleet("checkout", "shop")

	if v.Clusters != 2 {
		t.Fatalf("clusters = %d, want 2: a cluster that did not answer is still a cluster we asked", v.Clusters)
	}
	var found bool
	for _, r := range v.Rows {
		if r.Context == "prod-eu-west" {
			found = true
			if r.State != fleet.StateUnreachable {
				t.Errorf("state = %q, want %q", r.State, fleet.StateUnreachable)
			}
			if r.Note == "" {
				t.Error("an unreachable cluster names why")
			}
		}
	}
	if !found {
		t.Error("prod-eu-west was omitted; a blank row would read as 'not deployed there'")
	}
}

// TestFleetMatchesAcrossEnvSuffixedNamespaces proves the fleet view matches
// by the same identity the promotion matrix uses, so team-a-qa and
// team-a-prod are one app in two places rather than two different apps.
func TestFleetMatchesAcrossEnvSuffixedNamespaces(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa", apps: []fakeApp{{name: "checkout", ns: "team-a-qa", image: "reg/checkout:v2.14.0"}}},
		"prod-cluster": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "team-a-prod", image: "reg/checkout:v2.13.1"}}},
	})

	v := s.Fleet("checkout", "team-a-qa")

	if v.Present != 2 {
		t.Errorf("present = %d, want 2: team-a-qa and team-a-prod are one app in two places", v.Present)
	}
}

// TestARefusedReadIsDeniedNotAbsent proves the line the brief draws: clusters.Fetch
// never returns an error for an RBAC refusal, it records the kind in
// Snapshot.Partial. An app missing from that snapshot was never looked for, so
// calling it absent would be a lie.
func TestARefusedReadIsDeniedNotAbsent(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-cluster": {env: "prod", refusedKinds: []string{"Deployment"}},
	})

	v := s.Fleet("checkout", "shop")

	for _, r := range v.Rows {
		if r.Context != "prod-cluster" {
			continue
		}
		if r.State != fleet.StateDenied {
			t.Fatalf("state = %q, want %q: we were not allowed to look, which is not the same as it not being there", r.State, fleet.StateDenied)
		}
		if !strings.Contains(r.Note, "Deployment") {
			t.Errorf("note = %q, want it to name the kind that was refused", r.Note)
		}
	}
}

// TestFleetFoundNowhereStillNamesWhatItAsked proves a typo'd or genuinely
// absent app still renders one row per cluster asked, never an empty screen:
// never render an unknown window as an empty one.
func TestFleetFoundNowhereStillNamesWhatItAsked(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa"},
		"prod-cluster": {env: "prod"},
	})

	v := s.Fleet("typo", "shop")

	if v.Clusters != 2 {
		t.Errorf("clusters = %d, want 2: never render an unknown window as an empty one", v.Clusters)
	}
	if len(v.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(v.Rows))
	}
}

// TestFleetMergesAnUnreachableDuplicateWithItsReachableTwin pins the invariant
// fleet.mergeByCluster documents: ClusterID must come from the kubeconfig
// cluster entry, read before any connection attempt, on every path including
// the failure paths. Populate it from a successful connection instead and an
// unreachable duplicate context arrives with an empty ClusterID, fails to
// merge with its reachable twin, and inflates Clusters in exactly the case
// the merge exists to prevent.
func TestFleetMergesAnUnreachableDuplicateWithItsReachableTwin(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-primary": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-stale":   {env: "prod", unreachable: true},
	})
	// Both contexts in serviceWithContexts get distinct servers by
	// construction ("https://" + name), so point the stale one at the same
	// server the primary uses to simulate one cluster reachable under two
	// kubeconfig entries.
	for i := range s.cfg.Contexts {
		if s.cfg.Contexts[i].Name == "prod-stale" {
			s.cfg.Contexts[i].Server = "https://prod-primary"
		}
	}

	v := s.Fleet("checkout", "shop")

	if v.Clusters != 1 {
		t.Fatalf("clusters = %d, want 1: an unreachable duplicate context on the same server must merge with its reachable twin", v.Clusters)
	}
	if len(v.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(v.Rows))
	}
	if v.Rows[0].State != fleet.StatePresent {
		t.Errorf("state = %q, want %q: the reachable twin's answer must win the merge", v.Rows[0].State, fleet.StatePresent)
	}
	found := false
	for _, a := range v.Rows[0].Aliases {
		if a == "prod-stale" {
			found = true
		}
	}
	if !found {
		t.Errorf("aliases = %v, want prod-stale recorded as an alias of the merged row", v.Rows[0].Aliases)
	}
}

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/fleet"
)

// rowFor returns the row for one context, failing the test when the sweep lost
// it: every context asked gets a row, so a missing one is the bug itself.
func rowFor(t *testing.T, v fleet.View, ctx string) fleet.Row {
	t.Helper()
	for _, r := range v.Rows {
		if r.Context == ctx {
			return r
		}
	}
	t.Fatalf("no row for %q; rows = %+v", ctx, v.Rows)
	return fleet.Row{}
}

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
	for _, r := range v.Rows {
		if r.State != fleet.StateAbsent {
			t.Errorf("%s state = %q, want %q: nobody refused anything, the app simply is not there",
				r.Context, r.State, fleet.StateAbsent)
		}
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

// TestADigestPinnedImageIsPresentWithNoTagToCompare proves an image pinned by
// digest is reported as running with its version openly unknown. Fabricating a
// tag would be an invention, and letting it match the newest would render the
// deepest unknown on the screen as the calmest row on it.
func TestADigestPinnedImageIsPresentWithNoTagToCompare(t *testing.T) {
	const digest = "sha256:9f2c0a1b3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8"
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-cluster": {env: "prod", apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout@" + digest}}},
	})

	v := s.Fleet("checkout", "shop")

	r := rowFor(t, v, "prod-cluster")
	if r.State != fleet.StatePresent {
		t.Fatalf("state = %q, want %q: a pinned image is running, whatever it is called", r.State, fleet.StatePresent)
	}
	if r.Tag != "" {
		t.Errorf("tag = %q, want empty: there is no tag in a digest-pinned image to invent one from", r.Tag)
	}
	if !strings.Contains(r.Note, "version unknown") {
		t.Errorf("note = %q, want it to say the version is unknown", r.Note)
	}
	if r.Behind {
		t.Error("a version nobody can name is not a version behind: that would invent a direction")
	}
	if v.Newest != "v2.14.0" {
		t.Errorf("newest = %q, want v2.14.0: an untagged image must not become the yardstick", v.Newest)
	}
}

// TestAResolvedDigestLeavesNothingUnverified proves the digest actually arrives.
// It lives only in pod status, so a sweep that skips pods reports every row's
// digest as pending forever, and one tag running as two different images — the
// worst thing this screen can find — becomes impossible to find.
func TestAResolvedDigestLeavesNothingUnverified(t *testing.T) {
	const digest = "sha256:11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster": {env: "qa", apps: []fakeApp{{
			name: "checkout", ns: "shop",
			image:   "reg/checkout:v2.14.0",
			imageID: "reg/checkout@" + digest,
		}}},
	})

	v := s.Fleet("checkout", "shop")

	r := rowFor(t, v, "qa-cluster")
	if r.Digest != digest {
		t.Fatalf("digest = %q, want %q: the pod resolved it and the row must carry it", r.Digest, digest)
	}
	if r.DigestPending {
		t.Error("digestPending = true for a digest that arrived")
	}
	if v.DigestUnverified != 0 {
		t.Errorf("digestUnverified = %d, want 0: nothing is in flight once the pod answered", v.DigestUnverified)
	}
}

// TestOneTagRunningTwoDigestsIsFound is the top of the severity ladder and it
// is only reachable if digests arrive. Two clusters claiming one version while
// running different code is a defect, not a schedule.
func TestOneTagRunningTwoDigestsIsFound(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-us-east": {env: "prod", apps: []fakeApp{{
			name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0",
			imageID: "reg/checkout@sha256:aaaa000000000000000000000000000000000000000000000000000000000000",
		}}},
		"prod-eu-west": {env: "prod", apps: []fakeApp{{
			name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0",
			imageID: "reg/checkout@sha256:bbbb000000000000000000000000000000000000000000000000000000000000",
		}}},
	})

	v := s.Fleet("checkout", "shop")

	if !v.MutableTag {
		t.Fatal("mutableTag = false: one tag resolved to two digests and the screen said nothing")
	}
}

// TestADeniedRowNamesOnlyKindsThatWereRefused proves the note is a fact rather
// than an artefact of how much the sweep chose to read. Snapshot.Partial unions
// refusals with deliberate skips, and a row that names a kind nobody refused
// sends a developer to ask for permission they already have.
func TestADeniedRowNamesOnlyKindsThatWereRefused(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"qa-cluster":   {env: "qa"},
		"prod-cluster": {env: "prod", refusedKinds: []string{"CronJob"}},
	})

	v := s.Fleet("checkout", "shop")

	denied := rowFor(t, v, "prod-cluster")
	if denied.State != fleet.StateDenied {
		t.Fatalf("state = %q, want %q", denied.State, fleet.StateDenied)
	}
	if !strings.Contains(denied.Note, "CronJob") {
		t.Errorf("note = %q, want it to name CronJob, the kind that was refused", denied.Note)
	}
	for _, kind := range []string{"Pod", "ReplicaSet", "Deployment"} {
		if strings.Contains(denied.Note, kind) {
			t.Errorf("note = %q names %s, which nobody refused", denied.Note, kind)
		}
	}
	// Verb, resource and scope: what a Role rule is made of. A bare kind name
	// is half an RBAC message and the wrong half.
	if !strings.Contains(denied.Note, "list cronjobs") {
		t.Errorf("note = %q, want the verb and resource a developer would ask for", denied.Note)
	}

	clean := rowFor(t, v, "qa-cluster")
	if clean.State != fleet.StateAbsent {
		t.Fatalf("state = %q, want %q: nothing was refused here", clean.State, fleet.StateAbsent)
	}
	if strings.Contains(clean.Note, "Pod") {
		t.Errorf("note = %q mentions Pod, which was never asked for", clean.Note)
	}
}

// TestANamespaceWeNeverOpenedIsDeniedNotAbsent covers the case a cluster that
// refuses to list its namespaces produces: the read falls back to one namespace,
// and an app living anywhere else was never looked for. Calling that "not
// deployed here" is a claim nobody earned.
func TestANamespaceWeNeverOpenedIsDeniedNotAbsent(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-cluster": {env: "prod", refusedKinds: []string{"Namespace"}},
	})

	v := s.Fleet("checkout", "shop")

	r := rowFor(t, v, "prod-cluster")
	if r.State != fleet.StateDenied {
		t.Fatalf("state = %q, want %q: only the fallback namespace was read, and shop was not it",
			r.State, fleet.StateDenied)
	}
	if !strings.Contains(r.Note, "namespace list forbidden") {
		t.Errorf("note = %q, want the refusal that caused the fallback", r.Note)
	}
	if !strings.Contains(r.Note, "shop") {
		t.Errorf("note = %q, want it to name the namespace nobody opened", r.Note)
	}
}

// TestTheFallbackNamespaceWeDidReadIsAbsentNotDenied is the other half: a
// namespace-scoped read that covered exactly the namespace asked about did look,
// and found nothing. Denied there would be the same conflation in reverse.
func TestTheFallbackNamespaceWeDidReadIsAbsentNotDenied(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-cluster": {env: "prod", refusedKinds: []string{"Namespace"}},
	})

	v := s.Fleet("checkout", "default")

	r := rowFor(t, v, "prod-cluster")
	if r.State != fleet.StateAbsent {
		t.Fatalf("state = %q, want %q: default is the namespace the fallback read", r.State, fleet.StateAbsent)
	}
}

// TestTwoNamespacesInOneClusterSayWhichOneWon covers the arrangement
// StripEnvToken exists for, colocated in one cluster: team-a-qa and
// team-a-prod are one identity, a row holds one placement, and the match that
// did not win must not vanish without a word.
func TestTwoNamespacesInOneClusterSayWhichOneWon(t *testing.T) {
	s := serviceWithContexts(t, map[string]fakeCluster{
		"shared-cluster": {env: "prod", apps: []fakeApp{
			{name: "checkout", ns: "team-a-prod", image: "reg/checkout:v2.13.1"},
			{name: "checkout", ns: "team-a-qa", image: "reg/checkout:v2.14.0"},
		}},
	})

	v := s.Fleet("checkout", "team-a-qa")

	r := rowFor(t, v, "shared-cluster")
	if r.Namespace != "team-a-qa" {
		t.Errorf("namespace = %q, want team-a-qa: the namespace asked for wins the tie", r.Namespace)
	}
	if !strings.Contains(r.Note, "team-a-prod") {
		t.Errorf("note = %q, want it to name the match that was not shown", r.Note)
	}
}

// TestFleetSpendsTheTimeoutPerClusterNotAcrossThem pins what the flag means.
// Four clusters that each take a moment must cost one moment, not four: a
// serial sweep on one shared budget hands the last clusters an expired deadline
// and reports them unreachable, which invents the exact fact this screen exists
// to keep honest, and does it differently depending on ConnectOrder.
func TestFleetSpendsTheTimeoutPerClusterNotAcrossThem(t *testing.T) {
	const delay = 200 * time.Millisecond
	s := serviceWithContexts(t, map[string]fakeCluster{
		"prod-a": {env: "prod", slow: delay, apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-b": {env: "prod", slow: delay, apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-c": {env: "prod", slow: delay, apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
		"prod-d": {env: "prod", slow: delay, apps: []fakeApp{{name: "checkout", ns: "shop", image: "reg/checkout:v2.14.0"}}},
	})
	// Under the serial sweep this budget expires two clusters in; under a
	// per-cluster one every cluster gets the whole of it.
	s.timeout = 3 * delay

	start := time.Now()
	v := s.Fleet("checkout", "shop")
	elapsed := time.Since(start)

	if elapsed > s.timeout {
		t.Errorf("sweep took %v with a %v per-cluster timeout: four slow clusters spent one budget in turn",
			elapsed, s.timeout)
	}
	if v.Present != 4 {
		t.Errorf("present = %d, want 4", v.Present)
	}
	for _, r := range v.Rows {
		if r.State == fleet.StateUnreachable {
			t.Errorf("%s = unreachable (%q): a cluster that answered was reported as one that did not",
				r.Context, r.Note)
		}
	}
}

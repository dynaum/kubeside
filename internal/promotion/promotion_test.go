package promotion

import (
	"strings"
	"testing"
)

func envs() []Env {
	return []Env{{Name: "qa", Risk: "low"}, {Name: "stg", Risk: "medium"}, {Name: "prod", Risk: "high"}}
}

func at(t *testing.T, rows []Row, app string) Row {
	t.Helper()
	for _, r := range rows {
		if r.App == app {
			return r
		}
	}
	t.Fatalf("no row for %s", app)
	return Row{}
}

func cell(r Row, env string) Cell {
	for _, c := range r.Cells {
		if c.Env == env {
			return c
		}
	}
	return Cell{}
}

func running(env, app, tag string) Instance {
	return Instance{
		Env: env, App: app, Namespace: "team-a", Present: true,
		Image: "registry/" + app + ":" + tag, Tag: tag,
		Health: "healthy", Ready: "2/2",
	}
}

func TestMatchingVersionsAreInSync(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "checkout", "v2.13.1"),
		running("stg", "checkout", "v2.13.1"),
		running("prod", "checkout", "v2.13.1"),
	})

	r := at(t, rows, "checkout")
	for _, c := range r.Cells {
		if c.State != StateSame {
			t.Fatalf("cell = %+v, want everything in sync", c)
		}
	}
	if r.Drift != 0 {
		t.Errorf("drift = %d, want none", r.Drift)
	}
}

// Behind is the normal state of a promotion pipeline: a fix lands in qa first.
func TestAnEnvironmentBehindItsUpstreamIsMarkedBehind(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "payments", "v1.8.2"),
		running("stg", "payments", "v1.8.2"),
		running("prod", "payments", "v1.8.0"),
	})

	c := cell(at(t, rows, "payments"), "prod")
	if c.State != StateBehind {
		t.Fatalf("cell = %+v, want behind", c)
	}
	if c.Note == "" {
		t.Error("a behind cell should say what it is behind")
	}
}

// Ahead is worse than behind. Behind is a schedule; ahead is an out-of-band
// change, and the design gives it the error hue for exactly that reason.
func TestAnEnvironmentAheadOfItsUpstreamIsFlaggedHarder(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "search-indexer", "v3.1.0-rc4"),
		running("stg", "search-indexer", "v3.0.8"),
		running("prod", "search-indexer", "v3.1.0-rc4"),
	})

	c := cell(at(t, rows, "search-indexer"), "prod")
	if c.State != StateAhead {
		t.Fatalf("cell = %+v, want ahead", c)
	}
	if !c.Severe {
		t.Error("ahead of upstream should be marked severe; it is not a schedule, it is a surprise")
	}
}

// A tag nobody can order is still a difference worth showing; claiming a
// direction we cannot establish would be worse.
func TestUnorderableTagsDifferWithoutADirection(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "session-store", "sha-abc123"),
		running("stg", "session-store", "sha-def456"),
		running("prod", "session-store", "sha-def456"),
	})

	c := cell(at(t, rows, "session-store"), "stg")
	if c.State != StateDiffers {
		t.Fatalf("cell = %+v, want a difference with no claimed direction", c)
	}
}

// Same tag, different digest: a mutable tag means two environments are not
// running the same code, which is the defect this view exists to surface.
func TestSameTagDifferentDigestIsFlagged(t *testing.T) {
	qa := running("qa", "api-gateway", "v5.2.0")
	qa.Digest = "sha256:aaaa"
	prod := running("prod", "api-gateway", "v5.2.0")
	prod.Digest = "sha256:bbbb"
	stg := running("stg", "api-gateway", "v5.2.0")
	stg.Digest = "sha256:aaaa"

	rows := Build(envs(), []Instance{qa, stg, prod})

	c := cell(at(t, rows, "api-gateway"), "prod")
	if c.State != StateDigestDiffers {
		t.Fatalf("cell = %+v, want the digest mismatch surfaced", c)
	}
	if !c.Severe {
		t.Error("the same tag resolving differently is severe: the tag is lying")
	}
}

// Digests arrive after tags, so a cell without one yet must not claim the
// images match.
func TestAMissingDigestIsPendingNotEqual(t *testing.T) {
	qa := running("qa", "api-gateway", "v5.2.0")
	qa.Digest = "sha256:aaaa"
	prod := running("prod", "api-gateway", "v5.2.0")

	rows := Build(envs(), []Instance{qa, prod})

	c := cell(at(t, rows, "api-gateway"), "prod")
	if c.State != StateSame {
		t.Fatalf("cell = %+v; the tags match, so the tag comparison stands on its own", c)
	}
	if !c.DigestPending {
		t.Error("a cell with no digest yet should say the digest comparison is still pending")
	}
}

// "It is not there" and "I cannot see it" are different facts, and conflating
// them is dangerous.
func TestAbsentAndUnauthorizedRenderDifferently(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "notifications", "v0.9.1"),
		running("stg", "notifications", "v0.9.0"),
		{Env: "prod", App: "notifications", Namespace: "team-a", Denied: true, DeniedReason: "needs get deployments"},
	})

	r := at(t, rows, "notifications")
	if c := cell(r, "prod"); c.State != StateDenied || c.Note == "" {
		t.Fatalf("prod cell = %+v, want a denied cell that names what it needs", c)
	}

	rows = Build(envs(), []Instance{running("qa", "only-in-qa", "v1")})
	if c := cell(at(t, rows, "only-in-qa"), "prod"); c.State != StateAbsent {
		t.Fatalf("prod cell = %+v, want absent", c)
	}
}

// A cell nobody could read is not compared against, because an unknown is not
// an agreement.
func TestADeniedCellDoesNotBecomeTheUpstreamForTheNextOne(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "app", "v2"),
		{Env: "stg", App: "app", Namespace: "team-a", Denied: true, DeniedReason: "no access"},
		running("prod", "app", "v1"),
	})

	c := cell(at(t, rows, "app"), "prod")
	if c.State != StateBehind {
		t.Fatalf("prod cell = %+v; with stg unreadable, qa is the upstream that still says something", c)
	}
}

// Apps whose environments disagree float to the top: the matrix is read for
// disagreement, not for the alphabet.
func TestRowsSortByDriftThenName(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "aaa-in-sync", "v1"), running("stg", "aaa-in-sync", "v1"), running("prod", "aaa-in-sync", "v1"),
		running("qa", "zzz-drifted", "v2"), running("stg", "zzz-drifted", "v1"), running("prod", "zzz-drifted", "v1"),
	})

	if rows[0].App != "zzz-drifted" {
		t.Fatalf("first row = %q, want the drifted app on top", rows[0].App)
	}
}

// An app deployed under a slightly different namespace per environment is still
// one app; the promotion view exists to line those up.
func TestEnvironmentSuffixedNamespacesMatchAsOneApp(t *testing.T) {
	qa := running("qa", "checkout", "v2")
	qa.Namespace = "team-a-qa"
	prod := running("prod", "checkout", "v2")
	prod.Namespace = "team-a-prod"

	rows := Build(envs(), []Instance{qa, prod})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the two sides of one app on one row", len(rows))
	}
}

func TestSummaryCountsDriftAndAhead(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "a", "v2"), running("stg", "a", "v1"), running("prod", "a", "v1"),
		running("qa", "b", "v1"), running("stg", "b", "v1"), running("prod", "b", "v2"),
		running("qa", "c", "v1"), running("stg", "c", "v1"), running("prod", "c", "v1"),
	})

	s := Summarize(rows)
	if s.Apps != 3 || s.Drifted != 2 || s.Ahead != 1 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestTagIsReadFromTheImage(t *testing.T) {
	if got := TagOf("registry.example.com/team/app:v1.2.3"); got != "v1.2.3" {
		t.Errorf("tag = %q", got)
	}
	// A registry port is not a tag, and reading it as one would put ":5000" in
	// every cell of a self-hosted registry's matrix.
	if got := TagOf("registry.example.com:5000/team/app"); got != "latest" {
		t.Errorf("tag = %q, want the implicit latest", got)
	}
	if got := TagOf("app@sha256:abc"); got != "" {
		t.Errorf("tag = %q, want none for a digest-pinned image", got)
	}
	// An image that says nothing is not an image running latest. A metadata-only
	// read produces this, and "latest" would be a claim about which version is
	// live in the one view built to answer that.
	if got := TagOf(""); got != "" {
		t.Errorf("tag = %q, want none when the image is unknown", got)
	}
}

func TestIdentityMatchesAcrossEnvSuffixedNamespaces(t *testing.T) {
	qa := Identity("checkout", "team-a-qa")
	prod := Identity("checkout", "team-a-prod")
	if qa != prod {
		t.Errorf("Identity qa = %q, prod = %q; one team's namespace in two places is one app", qa, prod)
	}
}

func TestCompareTagsIsExported(t *testing.T) {
	if CompareTags("v1.2.0", "v1.10.0") >= 0 {
		t.Error("v1.2.0 should order below v1.10.0")
	}
	if CompareTags("sha-abc", "sha-def") != 0 {
		t.Error("build ids cannot be ordered, and claiming a direction invents one")
	}
}

func TestStripEnvTokenIsExported(t *testing.T) {
	if got := StripEnvToken("team-a-prod"); got != "team-a" {
		t.Errorf("StripEnvToken = %q, want team-a", got)
	}
}

func multiEnv() []Env {
	return []Env{
		{Name: "qa", Contexts: []string{"qa-cluster"}},
		{Name: "prod", Contexts: []string{"prod-us-east", "prod-eu-west", "prod-ap-south"}},
	}
}

func prodInstance(ctx, tag, digest string) Instance {
	return Instance{
		Env: "prod", Context: ctx, App: "checkout", Namespace: "shop",
		Present: true, Image: "reg/checkout:" + tag, Tag: tag, Digest: digest,
		Health: "healthy", Ready: "3/3",
	}
}

func cellFor(t *testing.T, rows []Row, env string) Cell {
	t.Helper()
	for _, c := range rows[0].Cells {
		if c.Env == env {
			return c
		}
	}
	t.Fatalf("no cell for env %q", env)
	return Cell{}
}

func TestAgreeingContextsCollapseToOneVersion(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		{Env: "qa", Context: "qa-cluster", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.14.0", Digest: "sha256:aa"},
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.13.1", "sha256:bb"),
		prodInstance("prod-ap-south", "v2.13.1", "sha256:bb"),
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateBehind {
		t.Errorf("state = %q, want %q: three clusters agreeing is one version", c.State, StateBehind)
	}
	if c.Tag != "v2.13.1" {
		t.Errorf("tag = %q, want v2.13.1", c.Tag)
	}
}

func TestDisagreeingContextsNeverCollapse(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.12.0", "sha256:cc"),
		prodInstance("prod-ap-south", "v2.13.1", "sha256:bb"),
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSplit {
		t.Fatalf("state = %q, want %q", c.State, StateSplit)
	}
	if c.Tag != "" {
		t.Errorf("tag = %q; a split cell must not pick a winner", c.Tag)
	}
	if !c.Severe {
		t.Error("a split prod is severe")
	}
	if c.Clusters != 3 {
		t.Errorf("clusters = %d, want 3", c.Clusters)
	}
	if !strings.Contains(c.Note, "2 versions") {
		t.Errorf("note = %q, want it to name the version count", c.Note)
	}
}

func TestSameTagDifferentDigestSplitsLoudly(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.13.1", "sha256:cc"),
		prodInstance("prod-ap-south", "v2.13.1", "sha256:bb"),
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSplit || !c.Severe {
		t.Fatalf("state = %q severe = %v, want a severe split", c.State, c.Severe)
	}
	if !strings.Contains(c.Note, "digest") {
		t.Errorf("note = %q, want it to name the mutable tag", c.Note)
	}
}

func TestPendingDigestDoesNotSplitAnAgreement(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.13.1", ""),
		prodInstance("prod-ap-south", "v2.13.1", "sha256:bb"),
	})
	c := cellFor(t, rows, "prod")
	if c.State == StateSplit {
		t.Fatal("a digest still in flight is not a disagreement")
	}
	if !c.DigestPending {
		t.Error("a missing digest must read as pending, never as a match")
	}
	if c.Digest != "" {
		t.Errorf("digest = %q; claiming one cluster's digest for the pair would be a guess", c.Digest)
	}
}

func TestPartialDeploymentSplits(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.13.1", "sha256:bb"),
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSplit {
		t.Fatalf("state = %q; deployed in two of three clusters is not deployed", c.State)
	}
	if !strings.Contains(c.Note, "2 of 3") {
		t.Errorf("note = %q, want it to name the coverage", c.Note)
	}
}

func TestUnreadableContextBlocksCollapse(t *testing.T) {
	envs := multiEnv()
	envs[1].Unreadable = []string{"prod-ap-south"}
	rows := Build(envs, []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.13.1", "sha256:bb"),
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSplit {
		t.Fatalf("state = %q; a cluster nobody could read is not an agreement", c.State)
	}
	if !strings.Contains(c.Note, "readable") {
		t.Errorf("note = %q, want it to say how many clusters answered", c.Note)
	}
}

func TestSplitCellIsNotUpstreamForTheNextColumn(t *testing.T) {
	envs := []Env{
		{Name: "qa", Contexts: []string{"qa-cluster"}},
		{Name: "stg", Contexts: []string{"stg-a", "stg-b"}},
		{Name: "prod", Contexts: []string{"prod-only"}},
	}
	rows := Build(envs, []Instance{
		{Env: "qa", Context: "qa-cluster", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.0.0", Digest: "sha256:aa"},
		{Env: "stg", Context: "stg-a", App: "checkout", Namespace: "shop", Present: true, Tag: "v3.0.0", Digest: "sha256:bb"},
		{Env: "stg", Context: "stg-b", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:cc"},
		{Env: "prod", Context: "prod-only", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.0.0", Digest: "sha256:aa"},
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSame {
		t.Errorf("prod state = %q, want %q: prod matches qa, and the split stg never became the basis", c.State, StateSame)
	}
}

func TestSingleContextEnvironmentIsUnchanged(t *testing.T) {
	envs := []Env{
		{Name: "qa", Contexts: []string{"qa-cluster"}},
		{Name: "prod", Contexts: []string{"prod-cluster"}},
	}
	rows := Build(envs, []Instance{
		{Env: "qa", Context: "qa-cluster", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.0.0", Digest: "sha256:aa"},
		{Env: "prod", Context: "prod-cluster", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:bb"},
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateBehind {
		t.Errorf("state = %q, want %q: one context per environment behaves exactly as before", c.State, StateBehind)
	}
}

// --- Regression tests added while fixing review findings on task #62 ---

// Finding 1: absent and denied cells are not comparisons, so they must not
// move the drift count. The old (pre-multi-cluster) code `continue`d past the
// drift increment for both; the multi-cluster rewrite briefly regressed this.
func TestAbsentAndDeniedDoNotCountAsDrift(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "notifications", "v0.9.1"),
		{Env: "stg", App: "notifications", Namespace: "team-a", Denied: true, DeniedReason: "needs get deployments"},
	})
	r := at(t, rows, "notifications")
	if cell(r, "stg").State != StateDenied {
		t.Fatalf("stg cell = %+v, want denied", cell(r, "stg"))
	}
	if cell(r, "prod").State != StateAbsent {
		t.Fatalf("prod cell = %+v, want absent", cell(r, "prod"))
	}
	if r.Drift != 0 {
		t.Fatalf("drift = %d, want 0: a denied cell and an absent cell are not disagreements to count", r.Drift)
	}
}

// A split cell is a genuine disagreement, unlike absent or denied, and must
// still count toward drift.
func TestSplitCellCountsAsDrift(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.12.0", "sha256:cc"),
		prodInstance("prod-ap-south", "v2.13.1", "sha256:bb"),
	})
	r := at(t, rows, "checkout")
	if r.Drift != 1 {
		t.Fatalf("drift = %d, want 1: a split prod cell is a real disagreement, and qa is absent (not counted)", r.Drift)
	}
}

// Finding 2: Identity() strips the environment token from the namespace, so
// "shop" and "shop-prod" in the SAME cluster/environment collapse to one app
// identity with two instances in one env. Env.Contexts is empty here, so
// clusters defaults to 1 and len(present) (2) exceeds it. The collapse guard
// must not treat that as "1 of 1 clusters"; it must decide on consensus
// alone.
func TestMoreInstancesThanClustersCollapsesOnAgreement(t *testing.T) {
	rows := Build([]Env{{Name: "prod"}}, []Instance{
		{Env: "prod", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
		{Env: "prod", App: "checkout", Namespace: "shop-prod", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
	})
	c := cellFor(t, rows, "prod")
	if c.State == StateSplit {
		t.Fatalf("state = %q; two agreeing instances must not split just because there are more of them than the environment declares clusters", c.State)
	}
	if c.Severe {
		t.Error("agreeing instances should not render severe")
	}
}

func TestMoreInstancesThanClustersSplitsOnDisagreementWithoutABogusCount(t *testing.T) {
	rows := Build([]Env{{Name: "prod"}}, []Instance{
		{Env: "prod", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
		{Env: "prod", App: "checkout", Namespace: "shop-prod", Present: true, Tag: "v2.0.0", Digest: "sha256:bb"},
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSplit || !c.Severe {
		t.Fatalf("state = %q severe = %v, want a severe split: two instances in one environment disagree", c.State, c.Severe)
	}
	if strings.Contains(c.Note, "1 cluster") || strings.Contains(c.Note, "of 1") {
		t.Errorf("note = %q; the environment declares one cluster but two instances exist here, so citing that count would be nonsense on a red cell", c.Note)
	}
	if c.Clusters != 0 {
		t.Errorf("clusters = %d, want 0: the declared cluster count cannot be trusted once instances outnumber it", c.Clusters)
	}
}

// Finding 3: a mutable tag (same tag, different digest) is a defect. Partial
// coverage is a schedule. When both are true at once, the defect must win the
// note, not the coverage fraction.
func TestDigestMismatchOutranksPartialCoverage(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.13.1", "sha256:cc"),
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSplit || !c.Severe {
		t.Fatalf("state = %q severe = %v, want a severe split", c.State, c.Severe)
	}
	if !strings.Contains(c.Note, "digest") {
		t.Errorf("note = %q, want the digest mismatch named even though coverage is also partial", c.Note)
	}
	if strings.Contains(c.Note, "of 3") {
		t.Errorf("note = %q; a mutable tag is a defect and should outrank a coverage fraction, not share the note with it", c.Note)
	}
}

// Finding 4: Summary.Split is a named deliverable with no assertions on it.
// Summarize breaks on the first split cell in a row, so a row with two split
// cells must still count once.
func TestSummaryCountsARowWithTwoSplitCellsOnce(t *testing.T) {
	twoSplitEnvs := []Env{
		{Name: "stg", Contexts: []string{"stg-a", "stg-b"}},
		{Name: "prod", Contexts: []string{"prod-a", "prod-b"}},
	}
	rows := Build(twoSplitEnvs, []Instance{
		{Env: "stg", Context: "stg-a", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
		{Env: "stg", Context: "stg-b", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.0.0", Digest: "sha256:bb"},
		{Env: "prod", Context: "prod-a", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
		{Env: "prod", Context: "prod-b", App: "checkout", Namespace: "shop", Present: true, Tag: "v3.0.0", Digest: "sha256:cc"},
	})
	s := Summarize(rows)
	if s.Split != 1 {
		t.Fatalf("split = %d, want 1: one row with two split cells still counts once", s.Split)
	}
}

func TestSummaryCountsOneSplitRow(t *testing.T) {
	rows := Build(multiEnv(), []Instance{
		prodInstance("prod-us-east", "v2.13.1", "sha256:bb"),
		prodInstance("prod-eu-west", "v2.12.0", "sha256:cc"),
		prodInstance("prod-ap-south", "v2.13.1", "sha256:bb"),
	})
	s := Summarize(rows)
	if s.Split != 1 {
		t.Fatalf("split = %d, want 1", s.Split)
	}
}

// Finding 5: the old code set Namespace on both absent and denied cells when
// the instance was known. resolve() dropped it.
func TestDeniedCellKeepsNamespace(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "notifications", "v0.9.1"),
		{Env: "prod", App: "notifications", Namespace: "team-a", Denied: true, DeniedReason: "needs get deployments"},
	})
	c := cell(at(t, rows, "notifications"), "prod")
	if c.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a: the instance is known, even though it is denied", c.Namespace)
	}
}

func TestAbsentCellKeepsNamespaceWhenTheInstanceIsKnown(t *testing.T) {
	rows := Build(envs(), []Instance{
		running("qa", "notifications", "v0.9.1"),
		{Env: "prod", App: "notifications", Namespace: "team-a", Present: false},
	})
	c := cell(at(t, rows, "notifications"), "prod")
	if c.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a: the instance is known even though it is not present", c.Namespace)
	}
}

// --- Regression tests added while fixing round-2 review findings ---

// Finding A: round 1's "more present instances than declared clusters"
// branch checked len(present) > clusters and decided on consensus alone,
// but returned before env.Unreadable was consulted. An environment with an
// unreadable context must never collapse to agreement, no matter how many
// instances are present, because the clusters nobody read might disagree.
// It must also never become upstream for the next column.
func TestUnreadableBlocksCollapseEvenWhenInstancesOutnumberClusters(t *testing.T) {
	env := Env{Name: "prod", Contexts: []string{"prod-a"}, Unreadable: []string{"prod-a"}}
	group := []Instance{
		{Env: "prod", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
		{Env: "prod", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
	}
	_, c, ok := resolve(env, group, nil)
	if ok {
		t.Fatal("ok = true; an environment with an unreadable context must never become upstream, even when the present instances agree and outnumber the declared clusters")
	}
	if c.State != StateSplit {
		t.Fatalf("state = %q, want split: unreadable must block collapse even when instances outnumber declared clusters", c.State)
	}
}

// Finding B: the digest-mismatch note in split() cited the declared cluster
// count, not the number of clusters that actually reported the tag. Round 1
// made this branch reachable under partial coverage, where those two numbers
// differ, and the note ends up describing a set of clusters that isn't the
// one it's talking about.
func TestDigestMismatchNoteCitesReportedClustersNotDeclared(t *testing.T) {
	env := Env{Name: "prod", Contexts: []string{"prod-a", "prod-b", "prod-c"}}
	group := []Instance{
		{Env: "prod", Context: "prod-a", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
		{Env: "prod", Context: "prod-b", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:bb"},
	}
	_, c, _ := resolve(env, group, nil)
	if !strings.Contains(c.Note, "across 2 clusters") {
		t.Errorf("note = %q, want it to cite the 2 clusters that actually reported the tag", c.Note)
	}
	if strings.Contains(c.Note, "across 3 clusters") {
		t.Errorf("note = %q; 3 is the declared total, not the count that reported the tag", c.Note)
	}
}

// Finding C: resolve() took group[0].Namespace unconditionally, so a denied
// instance with an unknown (empty) namespace ahead of a later instance that
// does know its namespace still dropped the namespace from the cell.
func TestNamespaceIsFirstNonEmptyInTheGroup(t *testing.T) {
	env := Env{Name: "prod"}
	group := []Instance{
		{Env: "prod", App: "notifications", Namespace: "", Denied: true, DeniedReason: "needs get deployments"},
		{Env: "prod", App: "notifications", Namespace: "team-a", Denied: true, DeniedReason: "still denied"},
	}
	_, c, _ := resolve(env, group, nil)
	if c.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a: the first non-empty namespace in the group, not always group[0]", c.Namespace)
	}
}

// --- Issue #75: an unread environment must not render "not deployed here" ---
//
// service.go builds its denied placeholders with an empty App name, and the
// `in.App != ""` filter strips them before promotion.Build ever sees them. So
// resolve()'s len(present) == 0 path saw denied == 0 for an environment
// nobody could read, and returned StateAbsent. Env.Unreadable now carries
// exactly the information resolve() was ignoring.

// A fully unreadable environment (every context in Unreadable) with no
// present instances must render denied, not absent, and must name how many
// clusters did not answer.
func TestFullyUnreadableEnvironmentIsDeniedNotAbsent(t *testing.T) {
	env := Env{Name: "prod", Contexts: []string{"prod-a", "prod-b", "prod-c"}, Unreadable: []string{"prod-a", "prod-b", "prod-c"}}
	_, c, ok := resolve(env, nil, nil)
	if c.State == StateAbsent {
		t.Fatalf("state = %q, want anything but absent: no context in this environment answered", c.State)
	}
	if c.State != StateDenied {
		t.Fatalf("state = %q, want denied: every context failed to answer, same fact as a single denied cluster", c.State)
	}
	if !strings.Contains(c.Note, "3") {
		t.Errorf("note = %q, want it to name how many clusters did not answer", c.Note)
	}
	if ok {
		t.Error("ok = true; a fully unreadable environment must never become the upstream for the next column")
	}
}

// The broader form of the same bug: an app absent from the READABLE clusters
// of a partially readable environment might still be running in the cluster
// nobody read. This must never render absent either.
func TestPartiallyUnreadableEnvironmentIsNotAbsentEither(t *testing.T) {
	env := Env{Name: "prod", Contexts: []string{"prod-a", "prod-b", "prod-c"}, Unreadable: []string{"prod-c"}}
	_, c, ok := resolve(env, nil, nil)
	if c.State == StateAbsent {
		t.Fatalf("state = %q, want anything but absent: prod-c never answered and might have it", c.State)
	}
	// Round-1 review finding: this test asserted only "not absent," so a
	// future refactor routing this case through StateSplit (a genuine
	// disagreement) instead of StateDenied (an unknown) would flip
	// Row.Drift, Summary.Split, and the matrix sort order, and this test
	// would stay green. Pin the exact state.
	if c.State != StateDenied {
		t.Fatalf("state = %q, want denied: the silent cluster is an unknown, not a disagreement observed among present instances", c.State)
	}
	if c.Note == "" {
		t.Fatal("a cell that cannot claim absence should say what was read and what was not")
	}
	if !strings.Contains(c.Note, "2") || !strings.Contains(c.Note, "1") {
		t.Errorf("note = %q, want it to name both the clusters read (2) and the cluster that did not answer (1)", c.Note)
	}
	// Round-1 review finding 3: the original note ("not found in the 2 of 3
	// clusters read; 1 did not answer and may have it") was the longest
	// string in the file and renders inside a matrix cell. Every other split()
	// note is terse; this one must carry the same two facts without the
	// padding.
	if c.Note != "not in the 2 clusters read; 1 did not answer" {
		t.Errorf("note = %q, want a terse note carrying the same two facts (clusters read, clusters unread)", c.Note)
	}
	if ok {
		t.Error("ok = true; a partially unreadable environment must never become the upstream for the next column")
	}
}

// Round-1 review finding 1: the drift behavior the commit and spec both rest
// on (routing the unread case through StateDenied avoids wrongly inflating
// drift) was never asserted. An app present in one environment, with the
// other environment partially unreadable and nothing present there, must not
// count as drift, and must not be counted as a split row either: the silent
// cluster is a fact nobody observed, not a disagreement.
func TestPartiallyUnreadableEnvironmentDoesNotInflateDriftOrSplit(t *testing.T) {
	envs := []Env{
		{Name: "qa", Contexts: []string{"qa-a"}},
		{Name: "prod", Contexts: []string{"p-a", "p-b", "p-c"}, Unreadable: []string{"p-c"}},
	}
	rows := Build(envs, []Instance{
		{Env: "qa", Context: "qa-a", App: "checkout", Namespace: "shop", Present: true, Tag: "v1.0.0", Digest: "sha256:aa"},
	})
	r := at(t, rows, "checkout")
	prod := cell(r, "prod")
	if prod.State != StateDenied {
		t.Fatalf("prod state = %q, want denied", prod.State)
	}
	if r.Drift != 0 {
		t.Fatalf("drift = %d, want 0: the unread cluster is an unknown, not a disagreement, and must not float this row up the matrix", r.Drift)
	}
	s := Summarize(rows)
	if s.Split != 0 {
		t.Fatalf("split = %d, want 0: a denied cell is not a split cell and must not be counted in Summary.Split", s.Split)
	}
}

// Regression guard: when every context in the environment actually answered
// and none of them have the app, "not deployed here" still stands. This is
// the ordinary case the fix must not disturb.
func TestFullyReadableAbsentEnvironmentStillReadsAbsent(t *testing.T) {
	env := Env{Name: "prod", Contexts: []string{"prod-a", "prod-b", "prod-c"}}
	_, c, ok := resolve(env, nil, nil)
	if c.State != StateAbsent || c.Note != "not deployed here" {
		t.Fatalf("cell = %+v, want absent/\"not deployed here\": every context answered and found nothing", c)
	}
	if ok {
		t.Error("ok = true; an absent cell must never become upstream")
	}
}

// A cell rendered denied because its environment was unreadable (fully or
// partially, with zero present instances) must not become the basis the
// next column is compared against, exactly like the pre-existing denied and
// absent paths.
func TestUnreadableEmptyEnvironmentDoesNotBecomeUpstream(t *testing.T) {
	envs := []Env{
		{Name: "qa", Contexts: []string{"qa-cluster"}},
		{Name: "stg", Contexts: []string{"stg-a", "stg-b", "stg-c"}, Unreadable: []string{"stg-a", "stg-b", "stg-c"}},
		{Name: "prod", Contexts: []string{"prod-cluster"}},
	}
	rows := Build(envs, []Instance{
		{Env: "qa", Context: "qa-cluster", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.0.0", Digest: "sha256:aa"},
		{Env: "prod", Context: "prod-cluster", App: "checkout", Namespace: "shop", Present: true, Tag: "v2.0.0", Digest: "sha256:aa"},
	})
	c := cellFor(t, rows, "prod")
	if c.State != StateSame {
		t.Errorf("prod state = %q, want %q: prod matches qa, and the unreadable stg column never became the basis", c.State, StateSame)
	}
	stg := cell(at(t, rows, "checkout"), "stg")
	if stg.State != StateDenied {
		t.Errorf("stg state = %q, want denied", stg.State)
	}
}

// Finding-5-style guard: the pre-existing denied path, where a real named
// instance carries Denied: true, must be untouched by this fix.
func TestNamedDeniedInstanceStillDeniedAlongsideUnreadable(t *testing.T) {
	env := Env{Name: "prod", Contexts: []string{"prod-a", "prod-b"}, Unreadable: []string{"prod-a"}}
	group := []Instance{
		{Env: "prod", App: "checkout", Namespace: "shop", Denied: true, DeniedReason: "needs get deployments"},
	}
	_, c, ok := resolve(env, group, nil)
	if c.State != StateDenied || c.Note != "needs get deployments" {
		t.Fatalf("cell = %+v, want the named denial reason preserved untouched", c)
	}
	if ok {
		t.Error("ok = true; a denied cell must never become upstream")
	}
}

// --- Round-1 review findings on task #75 ---

// Finding 2: the fully-unread branch compared unread against len(env.Contexts)
// but printed clusters (max(len(env.Contexts), 1)). An Env with no Contexts
// but a populated Unreadable — the shape most direct-call tests in this file
// use — printed a nonsensical "2 of 1 clusters did not answer". clusters must
// account for Unreadable outgrowing the declared Contexts, and the comparison
// and the message must use that same number.
func TestUnreadableCountNeverExceedsPrintedClusterCount(t *testing.T) {
	env := Env{Name: "prod", Unreadable: []string{"prod-a", "prod-b"}}
	_, c, ok := resolve(env, nil, nil)
	if c.State != StateDenied {
		t.Fatalf("state = %q, want denied", c.State)
	}
	if strings.Contains(c.Note, "of 1") {
		t.Errorf("note = %q; 1 is the bogus single-cluster default, not the 2 unread contexts actually named", c.Note)
	}
	if !strings.Contains(c.Note, "2 of 2") {
		t.Errorf("note = %q, want \"2 of 2 clusters did not answer\": the comparison and the message must use the same cluster count", c.Note)
	}
	if c.Clusters != 2 {
		t.Errorf("clusters = %d, want 2: the declared count must grow to cover the named unreadable contexts", c.Clusters)
	}
	if ok {
		t.Error("ok = true; a denied cell must never become upstream")
	}
}

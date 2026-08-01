package promotion

import "testing"

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

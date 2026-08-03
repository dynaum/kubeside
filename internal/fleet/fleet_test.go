package fleet

import "testing"

func present(ctx, server, env, tag, digest string) Placement {
	return Placement{
		Context: ctx, ClusterID: server, Env: env, Namespace: "shop",
		State: StatePresent, Image: "reg/checkout:" + tag, Tag: tag, Digest: digest,
		Health: "healthy", Ready: "3/3",
	}
}

func rowFor(t *testing.T, v View, ctx string) Row {
	t.Helper()
	for _, r := range v.Rows {
		if r.Context == ctx {
			return r
		}
	}
	t.Fatalf("no row for context %q", ctx)
	return Row{}
}

func TestNewestIsTheHighestTagAcrossPresentClusters(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("prod-us-east", "https://a", "prod", "v2.13.1", "sha256:bb"),
		present("prod-eu-west", "https://b", "prod", "v2.12.0", "sha256:cc"),
		present("qa-cluster", "https://c", "qa", "v2.14.0", "sha256:dd"),
	})
	if v.Newest != "v2.14.0" {
		t.Errorf("newest = %q, want v2.14.0", v.Newest)
	}
	if v.Behind != 2 {
		t.Errorf("behind = %d, want 2", v.Behind)
	}
	if !rowFor(t, v, "prod-eu-west").Behind {
		t.Error("prod-eu-west runs v2.12.0 and is behind")
	}
	if rowFor(t, v, "qa-cluster").Behind {
		t.Error("the newest cluster is not behind itself")
	}
}

func TestUnorderableTagsAreNeverCalledBehind(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "qa", "sha-abc123", "sha256:aa"),
		present("b", "https://b", "qa", "sha-def456", "sha256:bb"),
	})
	if v.Behind != 0 {
		t.Errorf("behind = %d, want 0: two build ids cannot be ordered, and claiming one is behind invents a direction", v.Behind)
	}
}

func TestSameTagDifferentDigestOutranksBehind(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "prod", "v2.13.1", "sha256:bb"),
		present("b", "https://b", "prod", "v2.13.1", "sha256:cc"),
		present("c", "https://c", "prod", "v2.12.0", "sha256:dd"),
	})
	if !v.MutableTag {
		t.Fatal("one tag resolving to two digests is the loudest state on the screen")
	}
	if v.Rows[0].Context != "a" && v.Rows[0].Context != "b" {
		t.Errorf("first row = %q; the mutable-tag clusters sort above the behind one", v.Rows[0].Context)
	}
}

func TestTwoContextsOnOneClusterMergeToOneRow(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		{Context: "prod-alias", ClusterID: "https://a", Env: "prod", State: StateUnreachable, Reason: "credentials expired"},
		present("prod-us-east", "https://a", "prod", "v2.13.1", "sha256:bb"),
	})
	if len(v.Rows) != 1 {
		t.Fatalf("rows = %d, want 1: one cluster reached by two contexts is one cluster", len(v.Rows))
	}
	if v.Rows[0].State != StatePresent {
		t.Errorf("state = %q, want %q: the context that answered wins over the one that did not", v.Rows[0].State, StatePresent)
	}
	if v.Clusters != 1 {
		t.Errorf("clusters = %d, want 1; counting it twice inflates the headline number", v.Clusters)
	}
}

func TestTheFiveStatesStayDistinct(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "qa", "v1.0.0", "sha256:aa"),
		{Context: "b", ClusterID: "https://b", Env: "stg", State: StateAbsent},
		{Context: "c", ClusterID: "https://c", Env: "prod", State: StateDenied, Reason: "pods list refused"},
		{Context: "d", ClusterID: "https://d", Env: "prod", State: StateUnreachable, Reason: "dial timeout"},
		{Context: "e", ClusterID: "https://e", Env: "prod", State: StatePending},
	})
	if v.Clusters != 5 {
		t.Fatalf("clusters = %d, want 5", v.Clusters)
	}
	if v.Present != 1 {
		t.Errorf("present = %d, want 1", v.Present)
	}
	seen := map[string]bool{}
	for _, r := range v.Rows {
		seen[r.State] = true
	}
	for _, s := range []string{StatePresent, StateAbsent, StateDenied, StateUnreachable, StatePending} {
		if !seen[s] {
			t.Errorf("state %q was collapsed into another; absent, denied, and unreachable are different facts", s)
		}
	}
}

func TestNoClusterRunsTheAppAtAll(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		{Context: "a", ClusterID: "https://a", Env: "qa", State: StateAbsent},
		{Context: "b", ClusterID: "https://b", Env: "prod", State: StateAbsent},
	})
	if v.Newest != "" {
		t.Errorf("newest = %q, want empty", v.Newest)
	}
	if v.Clusters != 2 || v.Present != 0 {
		t.Errorf("clusters = %d present = %d, want 2 and 0: the screen names what it asked", v.Clusters, v.Present)
	}
}

// C1: an unorderable tag must never become the yardstick, whatever order the
// caller hands the placements over in.
func TestOneUnorderableTagDoesNotSilenceTheDrift(t *testing.T) {
	orders := map[string][]Placement{
		"build id first": {
			present("a", "https://a", "qa", "sha-x", "sha256:aa"),
			present("b", "https://b", "stg", "v1.0.0", "sha256:bb"),
			present("c", "https://c", "prod", "v2.0.0", "sha256:cc"),
		},
		"build id last": {
			present("b", "https://b", "stg", "v1.0.0", "sha256:bb"),
			present("c", "https://c", "prod", "v2.0.0", "sha256:cc"),
			present("a", "https://a", "qa", "sha-x", "sha256:aa"),
		},
	}
	for name, ps := range orders {
		t.Run(name, func(t *testing.T) {
			v := Build("checkout", "shop", ps)
			if v.Newest != "v2.0.0" {
				t.Errorf("newest = %q, want v2.0.0: a tag nothing can be ordered against is not the newest version", v.Newest)
			}
			if v.Behind != 1 {
				t.Errorf("behind = %d, want 1: v1.0.0 is behind v2.0.0 whether or not a build id is in the fleet", v.Behind)
			}
			if !rowFor(t, v, "b").Behind {
				t.Error("b runs v1.0.0 while c runs v2.0.0 and is behind")
			}
			if rowFor(t, v, "a").Behind {
				t.Error("a build id is not behind anything; claiming it is invents a direction")
			}
			if got, want := rowFor(t, v, "a").Note, "runs sha-x, which cannot be ordered against v2.0.0"; got != want {
				t.Errorf("note = %q, want %q", got, want)
			}
			if got, want := rowFor(t, v, "b").Note, "behind v2.0.0"; got != want {
				t.Errorf("note = %q, want %q", got, want)
			}
			if got := rowFor(t, v, "c").Note; got != "" {
				t.Errorf("note = %q, want empty: c runs the newest version", got)
			}
		})
	}
}

// C1: with nothing orderable anywhere, there is no newest and no row claims to
// be on it.
func TestAllBuildIdsLeaveNoNewestAndNoBehind(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "qa", "sha-abc123", "sha256:aa"),
		present("b", "https://b", "qa", "sha-def456", "sha256:bb"),
	})
	if v.Newest != "" {
		t.Errorf("newest = %q, want empty: neither build id can be ordered against the other", v.Newest)
	}
	if v.Behind != 0 {
		t.Errorf("behind = %d, want 0", v.Behind)
	}
	if got, want := rowFor(t, v, "a").Note, "runs sha-abc123, which cannot be ordered against any other version here"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

// C2a: a fleet of digest-pinned images knows nothing, and must not render as a
// fleet that agrees.
func TestPresentWithoutATagIsNotUpToDate(t *testing.T) {
	untagged := func(ctx, server, digest string) Placement {
		return Placement{
			Context: ctx, ClusterID: server, Env: "prod", Namespace: "shop",
			State: StatePresent, Image: "reg/checkout@" + digest, Digest: digest,
			Health: "healthy", Ready: "3/3",
		}
	}
	v := Build("checkout", "shop", []Placement{
		untagged("a", "https://a", "sha256:aa"),
		untagged("b", "https://b", "sha256:bb"),
	})
	if v.MutableTag {
		t.Error("mutableTag = true, want false: there is no tag to call mutable")
	}
	if v.Behind != 0 {
		t.Errorf("behind = %d, want 0", v.Behind)
	}
	want := "version unknown: the image is pinned by digest, so there is no tag to compare"
	for _, ctx := range []string{"a", "b"} {
		r := rowFor(t, v, ctx)
		if r.Note != want {
			t.Errorf("%s note = %q, want %q", ctx, r.Note, want)
		}
		if r.MutableTag {
			t.Errorf("%s mutableTag = true, want false", ctx)
		}
	}
	if severity(v.Rows[0]) == 0 {
		t.Error("a cluster whose version nobody could name sorts with the up-to-date clusters")
	}
}

// C2b: the untagged row's note must not ship an empty version into prose.
func TestUntaggedClusterBesideAKnownNewest(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "prod", "v2.14.0", "sha256:aa"),
		{Context: "b", ClusterID: "https://b", Env: "prod", Namespace: "shop",
			State: StatePresent, Image: "reg/checkout@sha256:bb", Digest: "sha256:bb"},
	})
	if v.Newest != "v2.14.0" {
		t.Errorf("newest = %q, want v2.14.0", v.Newest)
	}
	got := rowFor(t, v, "b").Note
	if got == "runs , which cannot be ordered against v2.14.0" {
		t.Fatalf("note = %q: an empty tag reached the UI as prose", got)
	}
	if want := "version unknown: the image is pinned by digest, so there is no tag to compare"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
	if rowFor(t, v, "b").Behind {
		t.Error("a version nobody could read is not a version behind")
	}
}

// I1: a mutable tag is a defect wherever it happens, not only on the newest tag.
func TestMutableTagOnAnOlderTagIsStillADefect(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "prod", "v2.12.0", "sha256:aa"),
		present("b", "https://b", "prod", "v2.12.0", "sha256:zz"),
		present("c", "https://c", "prod", "v2.12.0", "sha256:aa"),
		present("d", "https://d", "qa", "v2.14.0", "sha256:dd"),
	})
	if !v.MutableTag {
		t.Fatal("one tag resolving to two digests is a defect on v2.12.0 as much as on the newest tag")
	}
	for _, ctx := range []string{"a", "b", "c"} {
		if !rowFor(t, v, ctx).MutableTag {
			t.Errorf("%s carries the conflicting tag and is not marked", ctx)
		}
	}
	if rowFor(t, v, "d").MutableTag {
		t.Error("d runs a tag with one digest and must not be marked")
	}
	if got, want := rowFor(t, v, "a").Note, "behind v2.14.0, and another cluster runs v2.12.0 with a different digest"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
	if v.Rows[0].Context == "d" {
		t.Error("the newest cluster sorts above a mutable tag; the defect is the louder fact")
	}
}

// I2: a caller-supplied DigestPending survives, and the view says how much of
// the answer rests on digests that never arrived.
func TestUnverifiedDigestsAreCountedAndNotOverwritten(t *testing.T) {
	claimed := present("a", "https://a", "prod", "v2.13.1", "sha256:aa")
	claimed.DigestPending = true
	v := Build("checkout", "shop", []Placement{
		claimed,
		present("b", "https://b", "prod", "v2.13.1", ""),
		present("c", "https://c", "prod", "v2.13.1", "sha256:cc"),
		{Context: "d", ClusterID: "https://d", Env: "prod", State: StateAbsent},
	})
	if !rowFor(t, v, "a").DigestPending {
		t.Error("a caller that says the digest is still in flight is ignored")
	}
	if !rowFor(t, v, "b").DigestPending {
		t.Error("a present cluster with no digest is pending")
	}
	if rowFor(t, v, "c").DigestPending {
		t.Error("c reported a digest and is not pending")
	}
	if v.DigestUnverified != 2 {
		t.Errorf("digestUnverified = %d, want 2: the headline rests on two digests nobody confirmed", v.DigestUnverified)
	}
}

// I3: a merged row says what it merged, through every reshuffle.
func TestMergedRowNamesEveryContextItMerged(t *testing.T) {
	t.Run("winner holds", func(t *testing.T) {
		v := Build("checkout", "shop", []Placement{
			{Context: "prod-alias", ClusterID: "https://a", Env: "prod", State: StateUnreachable, Reason: "credentials expired"},
			present("prod-us-east", "https://a", "prod", "v2.13.1", "sha256:bb"),
		})
		assertAliases(t, v.Rows[0], "prod-alias")
	})
	t.Run("three contexts, one cluster", func(t *testing.T) {
		v := Build("checkout", "shop", []Placement{
			{Context: "a", ClusterID: "https://a", Env: "prod", State: StateUnreachable},
			present("b", "https://a", "prod", "v2.13.1", "sha256:bb"),
			{Context: "c", ClusterID: "https://a", Env: "prod", State: StateDenied},
		})
		if len(v.Rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(v.Rows))
		}
		if v.Rows[0].Context != "b" {
			t.Errorf("context = %q, want b: the context that answered wins", v.Rows[0].Context)
		}
		assertAliases(t, v.Rows[0], "a", "c")
	})
	t.Run("winner changes twice", func(t *testing.T) {
		v := Build("checkout", "shop", []Placement{
			{Context: "a", ClusterID: "https://a", Env: "prod", State: StateUnreachable},
			{Context: "b", ClusterID: "https://a", Env: "prod", State: StateDenied},
			present("c", "https://a", "prod", "v2.13.1", "sha256:bb"),
		})
		if len(v.Rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(v.Rows))
		}
		if v.Rows[0].Context != "c" {
			t.Errorf("context = %q, want c", v.Rows[0].Context)
		}
		assertAliases(t, v.Rows[0], "b", "a")
	})
}

func assertAliases(t *testing.T, r Row, want ...string) {
	t.Helper()
	if len(r.Aliases) != len(want) {
		t.Fatalf("aliases = %v, want %v: a merged row says what it merged", r.Aliases, want)
	}
	for i, w := range want {
		if r.Aliases[i] != w {
			t.Errorf("aliases = %v, want %v", r.Aliases, want)
			return
		}
	}
}

// M5: merging must not write through the caller's slice.
func TestMergeDoesNotWriteThroughTheCallersAliasArray(t *testing.T) {
	backing := make([]string, 1, 4)
	backing[0] = "seed"
	winner := present("winner", "https://a", "prod", "v1.0.0", "sha256:aa")
	winner.Aliases = backing

	Build("checkout", "shop", []Placement{
		{Context: "loser", ClusterID: "https://a", Env: "prod", State: StateUnreachable},
		winner,
	})

	if grown := backing[:cap(backing)]; grown[1] != "" {
		t.Errorf("Build wrote %q into the caller's alias array", grown[1])
	}
}

// I4: every sentence the screen shows is a promise, so every sentence is tested.
func TestEveryStateNamesItselfInTheNote(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "qa", "v1.0.0", "sha256:aa"),
		{Context: "b", ClusterID: "https://b", Env: "stg", State: StateAbsent},
		{Context: "c", ClusterID: "https://c", Env: "prod", State: StateDenied, Reason: "pods list refused"},
		{Context: "d", ClusterID: "https://d", Env: "prod", State: StateUnreachable, Reason: "dial timeout"},
		{Context: "e", ClusterID: "https://e", Env: "prod", State: StatePending},
	})
	want := map[string]string{
		"a": "",
		"b": "not deployed here",
		"c": "pods list refused",
		"d": "dial timeout",
		"e": "asking",
	}
	for ctx, w := range want {
		if got := rowFor(t, v, ctx).Note; got != w {
			t.Errorf("%s note = %q, want %q", ctx, got, w)
		}
	}
}

// I4: with no reason given, the note still says something true.
func TestNotesFallBackWhenNoReasonArrives(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		{Context: "c", ClusterID: "https://c", Env: "prod", State: StateDenied},
		{Context: "d", ClusterID: "https://d", Env: "prod", State: StateUnreachable},
	})
	if got, want := rowFor(t, v, "c").Note, "not readable here"; got != want {
		t.Errorf("denied note = %q, want %q", got, want)
	}
	if got, want := rowFor(t, v, "d").Note, "the cluster did not answer"; got != want {
		t.Errorf("unreachable note = %q, want %q", got, want)
	}
}

// M4: a cluster nobody read outranks one whose older version you can see. The
// promotion matrix takes the same position for the same reason.
func TestUnreadableSortsAboveBehind(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("behind", "https://a", "prod", "v1.0.0", "sha256:aa"),
		{Context: "silent", ClusterID: "https://b", Env: "prod", State: StateUnreachable, Reason: "dial timeout"},
		present("newest", "https://c", "prod", "v2.0.0", "sha256:cc"),
	})
	if v.Rows[0].Context != "silent" {
		t.Errorf("first row = %q, want silent: the version we could not read might be worse than the old one we could", v.Rows[0].Context)
	}
	if v.Rows[1].Context != "behind" {
		t.Errorf("second row = %q, want behind", v.Rows[1].Context)
	}
}

// M2: a placement whose state nobody set is unknown, not healthy.
func TestUnrecognizedStateIsNotRenderedAsHealthy(t *testing.T) {
	v := Build("checkout", "shop", []Placement{
		present("a", "https://a", "prod", "v1.0.0", "sha256:aa"),
		{Context: "b", ClusterID: "https://b", Env: "prod"},
	})
	r := rowFor(t, v, "b")
	if r.Note == "" {
		t.Error("a row with no state renders blank beside a healthy one")
	}
	if r.State != StatePending {
		t.Errorf("state = %q, want %q: an unrecognized state is one nobody has answered yet", r.State, StatePending)
	}
	if v.Present != 1 {
		t.Errorf("present = %d, want 1", v.Present)
	}
}

func TestNoPlacementsAtAll(t *testing.T) {
	v := Build("checkout", "shop", nil)
	if v.Rows == nil {
		t.Error("rows is nil and marshals as null; an empty fleet is an empty list")
	}
	if v.Clusters != 0 || v.Present != 0 || v.Newest != "" {
		t.Errorf("clusters = %d present = %d newest = %q, want 0, 0, empty", v.Clusters, v.Present, v.Newest)
	}
}

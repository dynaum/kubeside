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

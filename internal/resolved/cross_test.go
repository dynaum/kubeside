package resolved

import "testing"

func side(values ...Value) Container {
	return Container{Name: "app", Values: values}
}

func inline(key, value string) Value {
	return Value{Key: key, Value: value, Source: Source{Kind: SourceInline}}
}

func secret(key, ref string, digest string) Value {
	return Value{Key: key, Masked: true, Digest: digest, Source: Source{Kind: SourceSecret, Ref: ref, Key: key}}
}

func rowFor(t *testing.T, rows []CrossRow, key string) CrossRow {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", key, rows)
	return CrossRow{}
}

func envs() (Env, Env) {
	return Env{Name: "stg", Risk: "medium"}, Env{Name: "prod", Risk: "high"}
}

func TestIdenticalValuesMatch(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(side(inline("TZ", "UTC")), side(inline("TZ", "UTC")), stg, prod)

	if r := rowFor(t, rows, "TZ"); r.Class != ClassMatch {
		t.Fatalf("row = %+v, want a match", r)
	}
}

// A key present on one side and not the other is the difference that breaks a
// promotion, and it is never a subtle one.
func TestAKeyOnlyOneSideHasIsMissing(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(side(inline("RETRY_LIMIT", "3")), side(), stg, prod)

	r := rowFor(t, rows, "RETRY_LIMIT")
	if r.Class != ClassMissing {
		t.Fatalf("row = %+v, want missing", r)
	}
	if r.Right != "" || !r.RightUnset {
		t.Errorf("row = %+v, the absent side must render as unset rather than empty", r)
	}
}

// Values that carry their environment are supposed to differ. Flagging those
// would bury the differences that matter under the ones that never do.
func TestEnvironmentSpecificValuesAreExpectedToDiffer(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(inline("DATABASE_URL", "postgres://stg-db.internal:5432/payments")),
		side(inline("DATABASE_URL", "postgres://prod-db.internal:5432/payments")),
		stg, prod,
	)

	r := rowFor(t, rows, "DATABASE_URL")
	if r.Class != ClassExpected {
		t.Fatalf("row = %+v, want expected", r)
	}
	if r.Reason == "" {
		t.Error("a classification should name the rule that produced it")
	}
}

// A plain flag differing between environments is what this screen is for.
func TestAnUnexplainedDifferenceIsDrift(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(inline("FEATURE_NEW_CHECKOUT", "true")),
		side(inline("FEATURE_NEW_CHECKOUT", "false")),
		stg, prod,
	)

	if r := rowFor(t, rows, "FEATURE_NEW_CHECKOUT"); r.Class != ClassDrift {
		t.Fatalf("row = %+v, want drift", r)
	}
}

// A value naming another environment is worse than drift: it is a value
// pointing at the wrong place.
func TestAValueNamingTheOtherEnvironmentIsDrift(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(inline("OTEL_ENDPOINT", "http://otel.stg:4317")),
		side(inline("OTEL_ENDPOINT", "http://otel.stg:4317")),
		stg, prod,
	)

	r := rowFor(t, rows, "OTEL_ENDPOINT")
	if r.Class != ClassSuspicious {
		t.Fatalf("row = %+v, want it flagged: prod points at stg", r)
	}
	if r.Reason == "" {
		t.Error("the reason should say what was noticed")
	}
}

// Identical is not always fine. A development setting that survived into a
// higher-risk environment is exactly what nobody notices until the disk fills.
func TestADevelopmentSettingThatMatchesInProdIsSuspicious(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(side(inline("LOG_LEVEL", "debug")), side(inline("LOG_LEVEL", "debug")), stg, prod)

	r := rowFor(t, rows, "LOG_LEVEL")
	if r.Class != ClassSuspicious {
		t.Fatalf("row = %+v, want suspicious", r)
	}
}

// The same rule must not fire between two low-risk environments, where a debug
// setting is simply correct.
func TestADevelopmentSettingBetweenLowRiskEnvironmentsIsFine(t *testing.T) {
	qa := Env{Name: "qa", Risk: "low"}
	dev := Env{Name: "dev", Risk: "low"}
	rows := CrossDiff(side(inline("LOG_LEVEL", "debug")), side(inline("LOG_LEVEL", "debug")), qa, dev)

	if r := rowFor(t, rows, "LOG_LEVEL"); r.Class != ClassMatch {
		t.Fatalf("row = %+v, want a plain match between two low-risk environments", r)
	}
}

// Capacity carried unchanged from staging into production is the other quiet
// failure this screen exists to surface.
func TestIdenticalCapacityAcrossRiskTiersIsSuspicious(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(side(inline("MAX_CONNECTIONS", "25")), side(inline("MAX_CONNECTIONS", "25")), stg, prod)

	if r := rowFor(t, rows, "MAX_CONNECTIONS"); r.Class != ClassSuspicious {
		t.Fatalf("row = %+v, want suspicious", r)
	}
}

// Prod credentials must never sit beside staging credentials on one screen.
func TestSecretsCompareByDigestNeverByValue(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(secret("STRIPE_SECRET_KEY", "payments-stripe", "sha256:4f2a1c9e")),
		side(secret("STRIPE_SECRET_KEY", "payments-stripe", "sha256:9b71ff02")),
		stg, prod,
	)

	r := rowFor(t, rows, "STRIPE_SECRET_KEY")
	if !r.Masked {
		t.Fatal("a secret row is not masked")
	}
	if r.Left != "" || r.Right != "" {
		t.Fatalf("row = %+v, a secret row must carry no values at all", r)
	}
	if r.LeftDigest == "" || r.RightDigest == "" {
		t.Fatal("a secret row should carry digests to compare")
	}
	// Two different credentials per environment is the correct arrangement.
	if r.Class != ClassExpected {
		t.Errorf("class = %q, want differing secrets treated as expected", r.Class)
	}
}

// The same credential in staging and production is a real finding, and one the
// tool can report without ever reading either value onto the screen.
func TestTheSameSecretInBothEnvironmentsIsSuspicious(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(secret("STRIPE_SECRET_KEY", "payments-stripe", "sha256:4f2a1c9e")),
		side(secret("STRIPE_SECRET_KEY", "payments-stripe", "sha256:4f2a1c9e")),
		stg, prod,
	)

	r := rowFor(t, rows, "STRIPE_SECRET_KEY")
	if r.Class != ClassSuspicious {
		t.Fatalf("row = %+v; one credential shared by staging and production is worth a flag", r)
	}
}

// Without permission to read one side there is no digest, and no comparison.
// Saying so beats implying the secrets match.
func TestSecretsWithoutDigestsAreNotCompared(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(secret("PASSWORD", "db", "")),
		side(secret("PASSWORD", "db", "")),
		stg, prod,
	)

	r := rowFor(t, rows, "PASSWORD")
	if r.Class != ClassUnknown {
		t.Fatalf("row = %+v, want no claim without digests", r)
	}
	if r.Reason == "" {
		t.Error("an uncompared secret should say why")
	}
}

func TestRowsAreOrderedByKey(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(inline("Z", "1"), inline("A", "1")),
		side(inline("M", "1")),
		stg, prod,
	)

	if len(rows) != 3 {
		t.Fatalf("rows = %d, want the union of both sides", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Key < rows[i-1].Key {
			t.Fatalf("rows are not ordered: %s before %s", rows[i-1].Key, rows[i].Key)
		}
	}
}

func TestSummaryCountsEachClass(t *testing.T) {
	stg, prod := envs()
	rows := CrossDiff(
		side(inline("A", "1"), inline("B", "1"), inline("TZ", "UTC")),
		side(inline("A", "2"), inline("TZ", "UTC")),
		stg, prod,
	)

	s := Summarize(rows)
	if s.Drift != 1 || s.Missing != 1 || s.Match != 1 {
		t.Fatalf("summary = %+v", s)
	}
}

// A pod IP differs on every comparison anybody ever runs. Reporting it as drift
// would bury the rows that mean something.
func TestDownwardAPIValuesAreExpectedToDiffer(t *testing.T) {
	stg, prod := envs()
	downward := func(value string) Value {
		return Value{Key: "POD_IP", Value: value, Source: Source{Kind: SourceDownward, Key: "status.podIP"}}
	}

	rows := CrossDiff(side(downward("10.244.0.6")), side(downward("10.244.0.44")), stg, prod)

	r := rowFor(t, rows, "POD_IP")
	if r.Class != ClassExpected {
		t.Fatalf("row = %+v, want expected", r)
	}
}

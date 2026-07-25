package guard

import (
	"strings"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/environments"
)

func env(name string, risk environments.Risk, write environments.WritePolicy) environments.Environment {
	return environments.Environment{Name: name, Risk: risk, Write: write, Hazard: risk == environments.RiskHigh}
}

func action() Action {
	return Action{
		Verb: "delete", Resource: "pod", Name: "checkout-1", Namespace: "team-a",
		Kubectl: "kubectl delete pod checkout-1 -n team-a",
		Blast:   Blast{Pods: 1, Summary: "one of four replicas"},
	}
}

func TestAllowedEnvironmentsNeedNoCeremony(t *testing.T) {
	g := New(nil)
	got := g.Check("qa1", env("qa", environments.RiskLow, environments.WriteAllow), action())

	if !got.Permitted {
		t.Fatalf("gate = %+v, want the action permitted", got)
	}
	if got.Require != RequireNothing {
		t.Errorf("require = %q, want no ceremony in a low-risk environment", got.Require)
	}
}

// Typing the resource name is the pattern people already know from deleting a
// GitHub repository, and it is what stops the wrong window from acting.
func TestConfirmEnvironmentsRequireTypingTheName(t *testing.T) {
	g := New(nil)
	got := g.Check("stg1", env("stg", environments.RiskMedium, environments.WriteConfirm), action())

	if !got.Permitted {
		t.Fatal("a confirm environment permits the action, with ceremony")
	}
	if got.Require != RequireTypedName {
		t.Fatalf("require = %q, want a typed confirmation", got.Require)
	}
	if got.Confirm != "checkout-1" {
		t.Errorf("confirm = %q, want the resource name to type", got.Confirm)
	}
}

// The environment name travels with every gate, because a dialog that does not
// say which cluster it is about is a dialog somebody will misread.
func TestEveryGateNamesTheEnvironment(t *testing.T) {
	g := New(nil)
	for _, e := range []environments.Environment{
		env("qa", environments.RiskLow, environments.WriteAllow),
		env("prod", environments.RiskHigh, environments.WriteDeny),
	} {
		got := g.Check("ctx", e, action())
		if got.Environment != e.Name {
			t.Errorf("gate for %s = %+v, should name its environment", e.Name, got)
		}
	}
}

func TestDenyRefusesAndSaysHowToChangeIt(t *testing.T) {
	g := New(nil)
	got := g.Check("prod1", env("prod", environments.RiskHigh, environments.WriteDeny), action())

	if got.Permitted {
		t.Fatal("a deny environment must not permit the action")
	}
	if !strings.Contains(got.Reason, "prod") {
		t.Errorf("reason = %q, should name the environment refusing", got.Reason)
	}
	// The policy lives in a file the user controls; saying so beats a dead end.
	if !strings.Contains(got.Reason, "config") {
		t.Errorf("reason = %q, should say where the policy comes from", got.Reason)
	}
}

// Break-glass is how Marina gets access under pressure without prod being
// permanently armed.
func TestBreakGlassIsLockedUntilUnlocked(t *testing.T) {
	g := New(nil)
	e := env("prod", environments.RiskHigh, environments.WriteBreakGlass)

	got := g.Check("prod1", e, action())
	if got.Permitted {
		t.Fatal("break-glass starts locked")
	}
	if got.Require != RequireBreakGlass {
		t.Fatalf("require = %q, want break-glass", got.Require)
	}
}

func TestBreakGlassNeedsAStatedReason(t *testing.T) {
	g := New(nil)

	if _, err := g.Unlock("prod1", "  "); err == nil {
		t.Fatal("unlocking without a reason was allowed")
	}
	if _, err := g.Unlock("prod1", "incident 4412, rolling back the bad config"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

func TestAnUnlockedEnvironmentStillRequiresTyping(t *testing.T) {
	g := New(nil)
	e := env("prod", environments.RiskHigh, environments.WriteBreakGlass)

	if _, err := g.Unlock("prod1", "incident 4412"); err != nil {
		t.Fatal(err)
	}
	got := g.Check("prod1", e, action())

	if !got.Permitted {
		t.Fatal("an unlocked environment permits the action")
	}
	// Unlocking is not a licence to stop reading. The typed name stays.
	if got.Require != RequireTypedName {
		t.Fatalf("require = %q, want the typed confirmation to survive unlocking", got.Require)
	}
}

// Fifteen minutes, then prod is locked again without anybody having to remember
// to re-lock it.
func TestBreakGlassExpires(t *testing.T) {
	clock := &fixedClock{now: time.Unix(0, 0)}
	g := New(clock)
	e := env("prod", environments.RiskHigh, environments.WriteBreakGlass)

	if _, err := g.Unlock("prod1", "incident 4412"); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(BreakGlassWindow - time.Minute)
	if !g.Check("prod1", e, action()).Permitted {
		t.Fatal("the window should still be open")
	}

	clock.now = clock.now.Add(2 * time.Minute)
	got := g.Check("prod1", e, action())
	if got.Permitted {
		t.Fatal("the window should have closed on its own")
	}
	if !strings.Contains(got.Reason, "expired") {
		t.Errorf("reason = %q, should say the window closed", got.Reason)
	}
}

// Unlocking one environment must not unlock another. The whole point is that
// prod is armed deliberately and separately.
func TestUnlockingIsPerEnvironment(t *testing.T) {
	g := New(nil)
	e := env("prod", environments.RiskHigh, environments.WriteBreakGlass)

	if _, err := g.Unlock("prod-eu", "incident 4412"); err != nil {
		t.Fatal(err)
	}
	if g.Check("prod-us", e, action()).Permitted {
		t.Fatal("unlocking prod-eu unlocked prod-us")
	}
}

// Before any write: what changes, how many pods, and the equivalent kubectl.
func TestGateCarriesTheBlastRadiusAndTheCommand(t *testing.T) {
	g := New(nil)
	got := g.Check("stg1", env("stg", environments.RiskMedium, environments.WriteConfirm), action())

	if got.Blast.Pods != 1 || got.Blast.Summary == "" {
		t.Fatalf("blast = %+v, want what this touches", got.Blast)
	}
	if !strings.Contains(got.Kubectl, "kubectl delete pod checkout-1") {
		t.Errorf("kubectl = %q, want the equivalent command", got.Kubectl)
	}
}

// An action with no blast radius computed is not an action with no blast
// radius. Rendering "0 pods" would be a claim nobody made.
func TestAnUncomputedBlastRadiusSaysSo(t *testing.T) {
	g := New(nil)
	a := action()
	a.Blast = Blast{}

	got := g.Check("stg1", env("stg", environments.RiskMedium, environments.WriteConfirm), a)
	if !got.Blast.Unknown {
		t.Fatalf("blast = %+v, want it marked unknown rather than zero", got.Blast)
	}
}

// An unlock is an event worth keeping: it is the record of somebody arming
// production, and it belongs on the timeline with its reason.
func TestUnlockReturnsTheRecordToKeep(t *testing.T) {
	clock := &fixedClock{now: time.Unix(1000, 0)}
	g := New(clock)

	rec, err := g.Unlock("prod1", "incident 4412, rolling back")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Reason != "incident 4412, rolling back" || rec.Context != "prod1" {
		t.Fatalf("record = %+v", rec)
	}
	if !rec.Until.After(clock.now) {
		t.Errorf("record = %+v, should carry when the window closes", rec)
	}
}

// An unclassified environment inherits prod-strength guardrails, because
// assuming an unfamiliar cluster is safe is the dangerous default.
func TestUnclassifiedEnvironmentsAreTreatedAsProd(t *testing.T) {
	g := New(nil)
	unknown := environments.Classify("atlantis")

	got := g.Check("atlantis", unknown, action())
	if got.Permitted {
		t.Fatalf("gate = %+v, want an unknown environment refused by default", got)
	}
}

// No control ever mutates more than one environment. The gate is per context by
// construction, and this is the test that says so out loud.
func TestGatesAreAlwaysScopedToOneEnvironment(t *testing.T) {
	g := New(nil)
	if _, err := g.Unlock("", "incident"); err == nil {
		t.Fatal("unlocking with no environment named was allowed")
	}
}

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/timeline"
)

func serviceFor(t *testing.T, body string, contexts ...kubeconfig.Context) *Service {
	t.Helper()

	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	conf, err := config.Load(config.Options{Path: p})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	kcfg := &kubeconfig.Config{Current: contexts[0].Name, Contexts: contexts}
	mgr := clusters.New(kcfg, nil, clusters.Options{})
	return NewService(kcfg, mgr, kubeconfig.Options{}, conf, time.Second)
}

// The rail colours and the hazard hatch come from the backend, so the browser
// never has to re-guess what an environment is.
func TestContextViewCarriesTheResolvedEnvironment(t *testing.T) {
	svc := serviceFor(t, `
environments:
  - name: qa
    risk: low
    color: green
    write: allow
    clusters: [https://api.example.com]
`, kubeconfig.Context{Name: "prod-us-east", IsCurrent: true, Server: "https://api.example.com"})

	views := svc.Contexts()
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	v := views[0]
	// The context is named prod; the config binds its cluster to qa. Config wins.
	if v.Environment != "qa" || v.Risk != "low" || v.Write != "allow" || v.Color != "green" {
		t.Fatalf("view = %+v, want the configured qa environment", v)
	}
	if v.Hazard {
		t.Error("a low-risk environment must not render as hazardous")
	}
}

func TestContextViewFallsBackToNameClassification(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "prod-us-east", IsCurrent: true, Server: "https://api"})

	v := svc.Contexts()[0]
	if v.Environment != "prod-us-east" || v.Risk != "high" || v.Write != "deny" {
		t.Fatalf("view = %+v, want prod guardrails inferred from the name", v)
	}
	if !v.Hazard {
		t.Error("a high-risk environment must carry the hazard marking")
	}
}

// An unrecognized name is never rendered as safe.
func TestUnclassifiedContextIsHazardous(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "atlantis", IsCurrent: true, Server: "https://api"})

	v := svc.Contexts()[0]
	if v.Risk != "high" || !v.Hazard || v.Write != "deny" {
		t.Fatalf("view = %+v, want an unclassified context treated as dangerous", v)
	}
}

// A config with no metrics preference leaves the probe to decide; an explicit
// none must switch the usage columns off rather than render zeroes.
func TestConfiguredMetricsSourceIsHonored(t *testing.T) {
	svc := serviceFor(t, "defaults:\n  metrics: none\n", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	info := svc.metricsInfo(t.Context(), "qa1")
	if info.Available {
		t.Fatalf("metrics = %+v, want unavailable when the config says none", info)
	}
	if info.Source != "none" {
		t.Errorf("source = %q, want none", info.Source)
	}
}

// The live half of the timeline: what kubeside watched happen while it ran.
func TestObservedRecordsAHealthTransition(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	before := AppView{Namespace: "team-a", Name: "checkout", Health: "healthy"}
	after := AppView{Namespace: "team-a", Name: "checkout", Health: "failed", Detail: "pod checkout-1 is in CrashLoopBackOff"}
	svc.Observed("qa1", before, after)

	got := svc.live.Entries(sessionKey("qa1", "team-a", "checkout"))
	if len(got) != 1 {
		t.Fatalf("entries = %+v, want the transition", got)
	}
	if got[0].Title != "healthy → failed" || got[0].Kind != timeline.KindHealth {
		t.Fatalf("entry = %+v", got[0])
	}
	if got[0].Detail != after.Detail {
		t.Errorf("detail = %q, should carry why", got[0].Detail)
	}
}

// Replica counts churn through every rollout. A timeline of them buries the one
// line that matters.
func TestObservedIgnoresAChangeThatIsNotHealth(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	before := AppView{Namespace: "team-a", Name: "checkout", Health: "healthy", Ready: "3/4"}
	after := AppView{Namespace: "team-a", Name: "checkout", Health: "healthy", Ready: "4/4"}
	svc.Observed("qa1", before, after)

	if got := svc.live.Entries(sessionKey("qa1", "team-a", "checkout")); len(got) != 0 {
		t.Fatalf("entries = %+v, want none for a replica change", got)
	}
}

// A first sighting has nothing to compare against, and "it appeared" is the
// app list's job, not the timeline's.
func TestObservedIgnoresAnAppSeenForTheFirstTime(t *testing.T) {
	svc := serviceFor(t, "", kubeconfig.Context{Name: "qa1", IsCurrent: true, Server: "https://api"})

	svc.Observed("qa1", AppView{}, AppView{Namespace: "team-a", Name: "new", Health: "progressing"})

	if got := svc.live.Entries(sessionKey("qa1", "team-a", "new")); len(got) != 0 {
		t.Fatalf("entries = %+v, want none for a first sighting", got)
	}
}

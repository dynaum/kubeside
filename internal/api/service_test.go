package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
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

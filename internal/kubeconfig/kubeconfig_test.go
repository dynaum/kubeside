package kubeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

const primary = `
apiVersion: v1
kind: Config
current-context: qa
clusters:
  - name: qa-cluster
    cluster:
      server: https://qa.example.internal:6443
  - name: prod-cluster
    cluster:
      server: https://prod.example.internal:6443
      insecure-skip-tls-verify: true
users:
  - name: dev
    user: {}
contexts:
  - name: qa
    context: {cluster: qa-cluster, user: dev, namespace: team-a}
  - name: prod
    context: {cluster: prod-cluster, user: dev}
`

const secondary = `
apiVersion: v1
kind: Config
current-context: stg
clusters:
  - name: stg-cluster
    cluster:
      server: https://stg.example.internal:6443
users:
  - name: dev
    user: {}
contexts:
  - name: stg
    context: {cluster: stg-cluster, user: dev, namespace: team-b}
`

func TestLoadsEveryContextNotJustCurrent(t *testing.T) {
	cfg, err := Load(Options{Precedence: []string{write(t, "config", primary)}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Contexts) != 2 {
		t.Fatalf("got %d contexts, want 2 (loading only current-context defeats multi-environment)", len(cfg.Contexts))
	}
	if cfg.Current != "qa" {
		t.Errorf("current = %q, want qa", cfg.Current)
	}
}

func TestPerContextFieldsAreCaptured(t *testing.T) {
	cfg, err := Load(Options{Precedence: []string{write(t, "config", primary)}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	qa, ok := cfg.Get("qa")
	if !ok {
		t.Fatal("context qa missing")
	}
	if qa.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a", qa.Namespace)
	}
	if qa.Server != "https://qa.example.internal:6443" {
		t.Errorf("server = %q", qa.Server)
	}
	if !qa.IsCurrent {
		t.Error("qa should be marked current")
	}
	if qa.Insecure {
		t.Error("qa is not insecure")
	}

	prod, _ := cfg.Get("prod")
	if prod.Namespace != "" {
		t.Errorf("prod namespace = %q, want empty", prod.Namespace)
	}
	if !prod.Insecure {
		t.Error("prod sets insecure-skip-tls-verify and must be flagged so the UI can warn")
	}
	if prod.IsCurrent {
		t.Error("prod is not the current context")
	}
}

func TestMergesTheKubeconfigChain(t *testing.T) {
	a := write(t, "a", primary)
	b := write(t, "b", secondary)

	cfg, err := Load(Options{Precedence: []string{a, b}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Contexts) != 3 {
		t.Fatalf("got %d contexts, want 3 merged", len(cfg.Contexts))
	}
	if _, ok := cfg.Get("stg"); !ok {
		t.Error("context from the second file was not merged")
	}
	// First file in the chain wins current-context, matching kubectl.
	if cfg.Current != "qa" {
		t.Errorf("current = %q, want qa from the first file", cfg.Current)
	}
	if len(cfg.Sources) != 2 {
		t.Errorf("sources = %v, want both files recorded", cfg.Sources)
	}
}

func TestExplicitPathOverridesTheChain(t *testing.T) {
	a := write(t, "a", primary)
	b := write(t, "b", secondary)

	cfg, err := Load(Options{ExplicitPath: b, Precedence: []string{a}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Contexts) != 1 || cfg.Contexts[0].Name != "stg" {
		t.Fatalf("contexts = %+v, want only stg from the explicit path", cfg.Contexts)
	}
}

func TestMissingFileInChainIsNotFatal(t *testing.T) {
	// A stale KUBECONFIG entry is common and must not stop the app loading.
	cfg, err := Load(Options{Precedence: []string{
		filepath.Join(t.TempDir(), "does-not-exist"),
		write(t, "config", primary),
	}})
	if err != nil {
		t.Fatalf("a missing file in the chain should be skipped, got: %v", err)
	}
	if len(cfg.Contexts) != 2 {
		t.Errorf("got %d contexts, want 2", len(cfg.Contexts))
	}
}

func TestEmptyConfigYieldsNoContextsNotAnError(t *testing.T) {
	cfg, err := Load(Options{Precedence: []string{filepath.Join(t.TempDir(), "absent")}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Contexts) != 0 {
		t.Errorf("want no contexts, got %d", len(cfg.Contexts))
	}
	if _, ok := cfg.CurrentContext(); ok {
		t.Error("no current context should be reported")
	}
}

func TestContextOrderIsStable(t *testing.T) {
	p := write(t, "config", primary)
	var first []string
	for i := 0; i < 25; i++ {
		cfg, err := Load(Options{Precedence: []string{p}})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		var got []string
		for _, c := range cfg.Contexts {
			got = append(got, c.Name)
		}
		if first == nil {
			first = got
			continue
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d order = %v, want stable %v (map iteration must not leak)", i, got, first)
			}
		}
	}
}

func TestCurrentContextHelper(t *testing.T) {
	cfg, err := Load(Options{Precedence: []string{write(t, "config", primary)}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cur, ok := cfg.CurrentContext()
	if !ok || cur.Name != "qa" {
		t.Fatalf("CurrentContext = %+v, %v; want qa", cur, ok)
	}
}

func TestLoadNeverWritesToKubeconfig(t *testing.T) {
	p := write(t, "config", primary)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(p)

	if _, err := Load(Options{Precedence: []string{p}}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	statAfter, _ := os.Stat(p)

	if string(before) != string(after) {
		t.Fatal("kubeconfig contents changed; it is read-only input")
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Fatal("kubeconfig mtime changed; kubeside must not touch the file")
	}
}

func TestRESTConfigForKnownContext(t *testing.T) {
	p := write(t, "config", primary)
	rc, err := RESTConfigFor(Options{Precedence: []string{p}}, "prod")
	if err != nil {
		t.Fatalf("RESTConfigFor: %v", err)
	}
	if rc.Host != "https://prod.example.internal:6443" {
		t.Errorf("host = %q, want the prod server", rc.Host)
	}
	if !rc.Insecure {
		t.Error("insecure-skip-tls-verify should carry through to the rest config")
	}
}

func TestRESTConfigForUnknownContextErrors(t *testing.T) {
	p := write(t, "config", primary)
	if _, err := RESTConfigFor(Options{Precedence: []string{p}}, "nope"); err == nil {
		t.Fatal("want an error for an unknown context")
	}
}

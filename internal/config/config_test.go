package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynaum/kubeside/internal/environments"
	"github.com/dynaum/kubeside/internal/kubeconfig"
)

// write puts a config file in a temp dir and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// isolate points every discovery path at an empty directory, so a config file
// on the developer's own machine cannot influence a test.
//
// Windows reads USERPROFILE where the others read HOME, and a test that sets
// only one of them silently reads the real home directory on the other.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("KUBESIDE_CONFIG", "")
	return home
}

func kctx(name, server string) kubeconfig.Context {
	return kubeconfig.Context{Name: name, Server: server}
}

// Zero-config first run is the product's default. No file is not an error, and
// classification still happens.
func TestNoFileIsNotAnError(t *testing.T) {
	isolate(t)

	c, err := Load(Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Path != "" {
		t.Errorf("Path = %q, want empty when no file exists", c.Path)
	}
	env := c.Environment(kctx("prod-us-east", "https://example"))
	if env.Risk != environments.RiskHigh {
		t.Errorf("risk = %v, want high from name classification", env.Risk)
	}
}

// The guarantee is that kubeside writes nothing on its own. Looking for a
// config must not create one, nor the directory it would live in.
func TestLoadCreatesNothingOnDisk(t *testing.T) {
	home := isolate(t)

	if _, err := Load(Options{}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Load created %v; kubeside writes nothing the user did not ask for", entries)
	}
}

func TestKubesideConfigEnvVarWins(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: qa\n    risk: low\n    contexts: [kind-local]\n")
	t.Setenv("KUBESIDE_CONFIG", p)

	c, err := Load(Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Path != p {
		t.Fatalf("Path = %q, want %q", c.Path, p)
	}
}

// A KUBESIDE_CONFIG pointing nowhere is a mistake worth reporting: the user
// asked for that file specifically, so silently falling back would hide it.
func TestMissingExplicitConfigIsAnError(t *testing.T) {
	isolate(t)
	t.Setenv("KUBESIDE_CONFIG", filepath.Join(t.TempDir(), "nope.yaml"))

	if _, err := Load(Options{}); err == nil {
		t.Fatal("a missing KUBESIDE_CONFIG file was accepted")
	}
}

func TestDiscoversXDGConfigHome(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(home, ".config", "kubeside")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("defaults:\n  metrics: none\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := Load(Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Path != p {
		t.Fatalf("Path = %q, want %q", c.Path, p)
	}
	if c.Defaults.Metrics != "none" {
		t.Errorf("metrics = %q", c.Defaults.Metrics)
	}
}

func TestEnvironmentBindsByContextName(t *testing.T) {
	isolate(t)
	p := write(t, `
environments:
  - name: staging
    risk: medium
    color: amber
    write: confirm
    contexts: [weird-name-nobody-can-classify]
`)

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := c.Environment(kctx("weird-name-nobody-can-classify", "https://a"))
	if env.Name != "staging" || env.Risk != environments.RiskMedium || env.Write != environments.WriteConfirm {
		t.Fatalf("environment = %+v, want the configured staging", env)
	}
	if env.Color != "amber" {
		t.Errorf("color = %q", env.Color)
	}
}

// Context names are personal; a shared config keys off the API server URL. When
// both bind, the cluster wins, because it survives every rename.
func TestClusterURLBeatsContextName(t *testing.T) {
	isolate(t)
	p := write(t, `
environments:
  - name: qa
    risk: low
    contexts: [prod-us-east]
  - name: prod
    risk: high
    write: deny
    clusters: [https://A1B2C3.gr7.us-east-1.eks.amazonaws.com]
`)

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := c.Environment(kctx("prod-us-east", "https://A1B2C3.gr7.us-east-1.eks.amazonaws.com"))
	if env.Name != "prod" {
		t.Fatalf("environment = %q, want prod: the cluster URL must beat the context name", env.Name)
	}
}

func TestClusterURLMatchIgnoresTrailingSlashAndCase(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: prod\n    risk: high\n    clusters: [\"https://API.example.com:6443/\"]\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if env := c.Environment(kctx("anything", "https://api.example.com:6443")); env.Name != "prod" {
		t.Fatalf("environment = %q, want prod", env.Name)
	}
}

// A context the file says nothing about still classifies by name, so adding one
// environment to a config does not blind kubeside to the rest.
func TestUnlistedContextFallsBackToClassification(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: qa\n    risk: low\n    contexts: [kind-local]\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := c.Environment(kctx("prod-eu-west", "https://b"))
	if env.Risk != environments.RiskHigh || env.Write != environments.WriteDeny {
		t.Fatalf("environment = %+v, want prod guardrails from the name", env)
	}
}

// Omitted fields inherit the classification of the environment's own name, so a
// file that only binds contexts does not silently drop to the safest-sounding
// defaults.
func TestOmittedFieldsInheritFromTheEnvironmentName(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: prod\n    contexts: [some-cluster]\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := c.Environment(kctx("some-cluster", "https://c"))
	if env.Risk != environments.RiskHigh || env.Write != environments.WriteDeny || env.Color != "red" {
		t.Fatalf("environment = %+v, want prod defaults inherited from the name", env)
	}
	if !env.Hazard {
		t.Error("a high-risk environment must keep its hazard marking")
	}
}

// An unrecognized name is not a licence to guess. It inherits prod guardrails,
// exactly as an unclassified context does.
func TestUnknownNameWithoutRiskIsTreatedAsHighRisk(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: atlantis\n    contexts: [some-cluster]\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := c.Environment(kctx("some-cluster", "https://c"))
	if env.Risk != environments.RiskHigh || env.Write != environments.WriteDeny {
		t.Fatalf("environment = %+v, want high risk and denied writes", env)
	}
}

func TestInvalidRiskIsRejectedByName(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: qa\n    risk: kinda\n")

	_, err := Load(Options{Path: p})
	if err == nil {
		t.Fatal("an unknown risk was accepted")
	}
	if !strings.Contains(err.Error(), "kinda") || !strings.Contains(err.Error(), "risk") {
		t.Errorf("error %q should name the bad value and the field", err)
	}
}

func TestInvalidWritePolicyIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: qa\n    write: whenever\n")

	_, err := Load(Options{Path: p})
	if err == nil || !strings.Contains(err.Error(), "whenever") {
		t.Fatalf("error = %v, want one naming the bad write policy", err)
	}
}

// A typo must fail loudly. Silently ignoring a misspelled key would leave a
// developer believing prod is guarded when it is not.
func TestUnknownFieldIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, "environmets:\n  - name: qa\n")

	if _, err := Load(Options{Path: p}); err == nil {
		t.Fatal("a misspelled top-level key was accepted")
	}
}

func TestDuplicateContextBindingIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, `
environments:
  - name: qa
    contexts: [shared]
  - name: prod
    contexts: [shared]
`)

	_, err := Load(Options{Path: p})
	if err == nil || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("error = %v, want one naming the doubly-bound context", err)
	}
}

func TestNamelessEnvironmentIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - risk: low\n")

	if _, err := Load(Options{Path: p}); err == nil {
		t.Fatal("an environment with no name was accepted")
	}
}

func TestNamespaceAxisBuildsAResolver(t *testing.T) {
	isolate(t)
	p := write(t, `
namespaceAxis:
  pattern: "{env}-{system}-{service}"
  systems: [acme]
`)

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Axis == nil {
		t.Fatal("no resolver built from namespaceAxis")
	}
	d := c.Axis.Decompose("qa-acme-checkout")
	if !d.Tenant || d.Environment != "qa" || d.Service != "checkout" {
		t.Fatalf("decomposition = %+v", d)
	}
	if d := c.Axis.Decompose("kube-system"); d.Tenant {
		t.Error("a platform namespace decomposed as a tenant")
	}
}

func TestNoNamespaceAxisMeansNoResolver(t *testing.T) {
	isolate(t)
	p := write(t, "defaults:\n  metrics: auto\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Axis != nil {
		t.Error("a resolver was invented without a configured axis")
	}
}

func TestBadNamespacePatternIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, "namespaceAxis:\n  pattern: \"\"\n  systems: [acme]\n")

	if _, err := Load(Options{Path: p}); err == nil {
		t.Fatal("an empty namespace pattern was accepted")
	}
}

func TestAppMatchOverridesAreParsed(t *testing.T) {
	isolate(t)
	p := write(t, `
apps:
  checkout:
    match:
      qa: team-a-qa/checkout
      prod: prod/checkout
`)

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ref, ok := c.AppRef("checkout", "qa")
	if !ok {
		t.Fatal("no override for checkout in qa")
	}
	if ref.Namespace != "team-a-qa" || ref.Name != "checkout" {
		t.Fatalf("ref = %+v", ref)
	}
	if _, ok := c.AppRef("checkout", "stg"); ok {
		t.Error("an override was invented for an environment the file did not name")
	}
}

func TestAppMatchWithoutANamespaceIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, "apps:\n  checkout:\n    match:\n      qa: checkout\n")

	_, err := Load(Options{Path: p})
	if err == nil || !strings.Contains(err.Error(), "checkout") {
		t.Fatalf("error = %v, want one naming the malformed match", err)
	}
}

func TestDefaultsAreRead(t *testing.T) {
	isolate(t)
	p := write(t, "defaults:\n  namespaces: [team-a, team-b]\n  metrics: prometheus\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Defaults.Namespaces) != 2 {
		t.Errorf("namespaces = %v", c.Defaults.Namespaces)
	}
	if c.Defaults.Metrics != "prometheus" {
		t.Errorf("metrics = %q", c.Defaults.Metrics)
	}
}

func TestInvalidMetricsSourceIsRejected(t *testing.T) {
	isolate(t)
	p := write(t, "defaults:\n  metrics: guesswork\n")

	_, err := Load(Options{Path: p})
	if err == nil || !strings.Contains(err.Error(), "guesswork") {
		t.Fatalf("error = %v, want one naming the bad metrics source", err)
	}
}

func TestUnsetMetricsDefaultsToAuto(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: qa\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Defaults.Metrics != "auto" {
		t.Errorf("metrics = %q, want auto", c.Defaults.Metrics)
	}
}

func TestEmptyFileIsValid(t *testing.T) {
	isolate(t)
	p := write(t, "")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Path != p || c.Axis != nil || len(c.Environments) != 0 {
		t.Errorf("config = %+v, want an empty one", c)
	}
}

// The whole documented example must parse, or the docs are lying.
func TestDocumentedExampleParses(t *testing.T) {
	isolate(t)
	p := write(t, `
environments:
  - name: qa
    risk: low
    color: green
    write: allow
    contexts: [kind-local, qa-cluster]
  - name: stg
    risk: medium
    color: amber
    write: confirm
    contexts: [staging-eks]
  - name: prod
    risk: high
    color: red
    write: deny
    contexts: [prod-us-east, prod-eu-west]
    clusters:
      - https://A1B2C3.gr7.us-east-1.eks.amazonaws.com

apps:
  checkout:
    match:
      qa: team-a-qa/checkout
      stg: team-a/checkout-svc
      prod: prod/checkout

defaults:
  namespaces: []
  metrics: auto

namespaceAxis:
  pattern: "{env}-{system}-{service}"
  systems: [acme]
`)

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("the documented example does not parse: %v", err)
	}
	if len(c.Environments) != 3 {
		t.Errorf("environments = %d, want 3", len(c.Environments))
	}
	if env := c.Environment(kctx("staging-eks", "https://x")); env.Name != "stg" {
		t.Errorf("staging-eks resolved to %q", env.Name)
	}
	if c.Axis == nil {
		t.Error("no namespace resolver")
	}
}

func TestBreakGlassWritePolicyIsAccepted(t *testing.T) {
	isolate(t)
	p := write(t, "environments:\n  - name: prod\n    risk: high\n    write: break-glass\n    contexts: [p]\n")

	c, err := Load(Options{Path: p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if env := c.Environment(kctx("p", "https://y")); env.Write != environments.WriteBreakGlass {
		t.Errorf("write = %v, want break-glass", env.Write)
	}
}

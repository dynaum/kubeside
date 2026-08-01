package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
)

func cfg() *kubeconfig.Config {
	return &kubeconfig.Config{
		Current: "stg",
		Contexts: []kubeconfig.Context{
			{Name: "prod"}, {Name: "qa"}, {Name: "stg", IsCurrent: true},
		},
	}
}

// Asking for help is not a failure. It exits 0 and writes to stdout, so
// `kubeside --help | less` works and a wrapper script does not abort.
func TestHelpSucceedsAndWritesUsageToStdout(t *testing.T) {
	var out, errOut strings.Builder
	if err := run([]string{"--help"}, &out, &errOut); err != nil {
		t.Fatalf("run --help: %v, want nil", err)
	}
	if !strings.Contains(out.String(), "Usage of kubeside:") {
		t.Fatalf("usage did not reach stdout, got %q", out.String())
	}
	if errOut.String() != "" {
		t.Fatalf("help wrote to stderr: %q", errOut.String())
	}
}

// --serve is post-v1 in docs/06-roadmap.md. A flag whose only behaviour is an
// error should not be listed as if it were an option.
func TestHelpDoesNotAdvertiseServe(t *testing.T) {
	var out, errOut strings.Builder
	if err := run([]string{"--help"}, &out, &errOut); err != nil {
		t.Fatalf("run --help: %v", err)
	}
	if strings.Contains(out.String(), "serve") {
		t.Fatalf("usage still advertises -serve:\n%s", out.String())
	}
}

func TestServeIsNotAFlag(t *testing.T) {
	var out, errOut strings.Builder
	err := run([]string{"--serve"}, &out, &errOut)
	if err == nil {
		t.Fatal("run --serve: nil error, want an unknown-flag error")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("run --serve: %v, want an unknown-flag error", err)
	}
	if !strings.Contains(errOut.String(), "Usage of kubeside:") {
		t.Fatalf("a bad flag printed no usage, got %q", errOut.String())
	}
}

func TestFilterContextsEmptyKeepsEverything(t *testing.T) {
	got, err := filterContexts(cfg(), "")
	if err != nil {
		t.Fatalf("filterContexts: %v", err)
	}
	if len(got.Contexts) != 3 {
		t.Fatalf("got %d contexts, want all 3", len(got.Contexts))
	}
}

func TestFilterContextsSingle(t *testing.T) {
	got, err := filterContexts(cfg(), "qa")
	if err != nil {
		t.Fatalf("filterContexts: %v", err)
	}
	if len(got.Contexts) != 1 || got.Contexts[0].Name != "qa" {
		t.Fatalf("contexts = %+v, want only qa", got.Contexts)
	}
	// stg did not survive, so qa becomes the connect-order head.
	if got.Current != "qa" {
		t.Errorf("current = %q, want qa", got.Current)
	}
}

func TestFilterContextsCommaSeparatedAndDeduped(t *testing.T) {
	got, err := filterContexts(cfg(), " qa , stg ,qa")
	if err != nil {
		t.Fatalf("filterContexts: %v", err)
	}
	if len(got.Contexts) != 2 {
		t.Fatalf("got %d contexts, want qa and stg", len(got.Contexts))
	}
	if got.Current != "stg" {
		t.Errorf("current = %q, want stg preserved because it survived the filter", got.Current)
	}
}

// A typo must not look like a cluster with no apps.
func TestFilterContextsUnknownNameErrorsAndListsOptions(t *testing.T) {
	_, err := filterContexts(cfg(), "qaa")
	if err == nil {
		t.Fatal("want an error for an unknown context")
	}
	msg := err.Error()
	for _, want := range []string{"qaa", "prod", "qa", "stg"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q so the typo is obvious", msg, want)
		}
	}
}

func TestFilterContextsIgnoresEmptyEntries(t *testing.T) {
	got, err := filterContexts(cfg(), ",,")
	if err != nil {
		t.Fatalf("filterContexts: %v", err)
	}
	if len(got.Contexts) != 3 {
		t.Fatalf("a list of only separators should be treated as no filter, got %d", len(got.Contexts))
	}
}

func TestCredentialHelpNamesTheLoginCommand(t *testing.T) {
	var sb strings.Builder
	printCredentialHelp(&sb, kubeconfig.Context{
		Name: "prod",
		Exec: &kubeconfig.ExecConfig{
			Command: "aws",
			Args:    []string{"eks", "get-token", "--profile", "prod-admin"},
		},
	}, nil)
	if !strings.Contains(sb.String(), "aws sso login --profile prod-admin") {
		t.Fatalf("want the login command, got:\n%s", sb.String())
	}
}

func TestCredentialHelpFallsBackToTheConfiguredCommand(t *testing.T) {
	var sb strings.Builder
	printCredentialHelp(&sb, kubeconfig.Context{
		Name: "prod",
		Exec: &kubeconfig.ExecConfig{Command: "acme-auth", Args: []string{"token"}},
	}, nil)
	got := sb.String()
	if !strings.Contains(got, "acme-auth token") {
		t.Fatalf("want the configured command echoed, got:\n%s", got)
	}
	if strings.Contains(got, "sso login") {
		t.Fatal("must not invent a login command for an unknown tool")
	}
}

func TestCredentialHelpWithoutExecBlock(t *testing.T) {
	var sb strings.Builder
	printCredentialHelp(&sb, kubeconfig.Context{Name: "prod"}, nil)
	if !strings.Contains(sb.String(), "does not use a credential plugin") {
		t.Fatalf("want a non-plugin explanation, got:\n%s", sb.String())
	}
}

// A missing plugin binary and an expired session are different problems.
// Telling someone to run a login command for a tool they have not installed
// wastes their time.
func TestCredentialHelpDistinguishesMissingBinary(t *testing.T) {
	var sb strings.Builder
	printCredentialHelp(&sb, kubeconfig.Context{
		Name: "prod",
		Exec: &kubeconfig.ExecConfig{Command: "/nonexistent/aws", Args: []string{"eks", "get-token"}},
	}, errors.New(`getting credentials: exec: fork/exec /nonexistent/aws: no such file or directory`))

	got := sb.String()
	if !strings.Contains(got, "not installed or not on PATH") {
		t.Fatalf("want a missing-binary explanation, got:\n%s", got)
	}
	if strings.Contains(got, "sso login") {
		t.Fatal("must not suggest re-authenticating when the binary is absent")
	}
}

func TestIsMissingExecutable(t *testing.T) {
	if !isMissingExecutable(errors.New("exec: \"aws\": executable file not found in $PATH")) {
		t.Error("PATH lookup failure should count as missing")
	}
	if isMissingExecutable(errors.New("exit status 255")) {
		t.Error("a non-zero exit is a rejected credential, not a missing binary")
	}
	if isMissingExecutable(nil) {
		t.Error("nil is not a missing binary")
	}
}

// The terminal list names each context's environment, so the classification a
// config file changes is visible without opening a browser.
func TestPrintContextNamesTheEnvironment(t *testing.T) {
	var sb strings.Builder
	mgr := clusters.New(cfg(), nil, clusters.Options{})
	conf, err := config.Load(config.Options{Path: writeConfig(t, "environments:\n  - name: qa\n    risk: low\n    contexts: [prod]\n")})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	// The context is named prod but the config binds it to qa. The printout
	// must follow the config, not the name.
	printContext(&sb, mgr, conf, kubeconfig.Context{Name: "prod"}, result{})
	got := sb.String()
	if !strings.Contains(got, "qa") || !strings.Contains(got, "low risk") {
		t.Fatalf("want the configured environment in the header, got:\n%s", got)
	}
}

func TestPrintContextFallsBackToNameClassification(t *testing.T) {
	var sb strings.Builder
	mgr := clusters.New(cfg(), nil, clusters.Options{})
	conf, err := config.Load(config.Options{Path: writeConfig(t, "")})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	printContext(&sb, mgr, conf, kubeconfig.Context{Name: "prod-us-east"}, result{})
	if got := sb.String(); !strings.Contains(got, "high risk") {
		t.Fatalf("an unconfigured prod context must still read as high risk, got:\n%s", got)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// The URL kubeside opens carries the session token in a query string, and every
// platform has to hand it to a browser intact.
func TestBrowserCommandPerPlatform(t *testing.T) {
	const url = "http://127.0.0.1:7654/?t=abc-123_XYZ"

	for _, c := range []struct {
		goos string
		want string
	}{
		{"darwin", "open"},
		{"windows", "rundll32"},
		{"linux", "xdg-open"},
		{"freebsd", "xdg-open"},
	} {
		cmd, args := browserCommand(c.goos, url)
		if cmd != c.want {
			t.Errorf("%s: command = %q, want %q", c.goos, cmd, c.want)
		}
		if args[len(args)-1] != url {
			t.Errorf("%s: args = %v, the URL must arrive whole", c.goos, args)
		}
	}
}

// "cmd /c start" reads its first quoted argument as a window title, which
// mangles exactly the URL kubeside opens.
func TestWindowsDoesNotUseStart(t *testing.T) {
	cmd, args := browserCommand("windows", "http://127.0.0.1:7654/?t=x")
	if cmd == "cmd" || strings.Contains(strings.Join(args, " "), "start") {
		t.Fatalf("command = %q %v; start mangles a URL with a query string", cmd, args)
	}
}

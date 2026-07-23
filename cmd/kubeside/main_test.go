package main

import (
	"errors"
	"strings"
	"testing"

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

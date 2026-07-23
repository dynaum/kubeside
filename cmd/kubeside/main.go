// Command kubeside is a Kubernetes client scoped to the application developer.
//
// This is the M0 spike: it prints an app list to the terminal so a human can
// judge whether grouping produces apps they recognise. No UI until the apps
// feature. kubeside writes nothing to disk.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "kubeside:", err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("kubeside", flag.ContinueOnError)
	fs.SetOutput(errOut)
	showVersion := fs.Bool("version", false, "print version and exit")
	serve := fs.Bool("serve", false, "run in-cluster as a team web UI")
	kubeconfigPath := fs.String("kubeconfig", "", "explicit kubeconfig path")
	contextList := fs.String("context", "", "only use these kubeconfig contexts (comma-separated)")
	profile := fs.String("profile", "", "AWS_PROFILE for exec credential plugins")
	timeout := fs.Duration("timeout", 15*time.Second, "per-cluster connect and fetch timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintln(out, version.String())
		return nil
	}
	if *serve {
		return fmt.Errorf("--serve is not implemented yet; see docs/06-roadmap.md")
	}

	// --profile only sets the environment inherited by credential plugins.
	// kubeconfig is never edited.
	if *profile != "" {
		if err := os.Setenv("AWS_PROFILE", *profile); err != nil {
			return fmt.Errorf("set AWS_PROFILE: %w", err)
		}
	}

	opts := kubeconfig.Options{ExplicitPath: *kubeconfigPath}
	cfg, err := kubeconfig.Load(opts)
	if err != nil {
		return err
	}
	if len(cfg.Contexts) == 0 {
		fmt.Fprintln(out, "No kubeconfig contexts found.")
		fmt.Fprintln(out, "kubeside reads the kubeconfig you already have; nothing to import.")
		return nil
	}

	if cfg, err = filterContexts(cfg, *contextList); err != nil {
		return err
	}

	mgr := clusters.New(cfg, clusters.KubeConnector{Opts: opts}, clusters.Options{})
	defer mgr.Close()

	fmt.Fprintf(out, "kubeside %s\n", version.String())
	fmt.Fprintf(out, "%d %s from %s\n\n", len(cfg.Contexts), plural(len(cfg.Contexts), "context", "contexts"), strings.Join(cfg.Sources, ", "))

	results := gather(context.Background(), mgr, cfg, *timeout)
	for _, name := range mgr.ConnectOrder() {
		kctx, _ := cfg.Get(name)
		printContext(out, mgr, kctx, results[name])
	}
	return nil
}

type result struct {
	snap clusters.Snapshot
	err  error
}

// filterContexts narrows the config to the named contexts.
//
// An unknown name is an error listing what is available, never a silent empty
// result: a typo should not look like a cluster with no apps.
func filterContexts(cfg *kubeconfig.Config, list string) (*kubeconfig.Config, error) {
	if strings.TrimSpace(list) == "" {
		return cfg, nil
	}

	want := map[string]bool{}
	var order []string
	for _, raw := range strings.Split(list, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := cfg.Get(name); !ok {
			var have []string
			for _, c := range cfg.Contexts {
				have = append(have, c.Name)
			}
			return nil, fmt.Errorf("unknown context %q; available: %s", name, strings.Join(have, ", "))
		}
		if !want[name] {
			want[name] = true
			order = append(order, name)
		}
	}
	if len(order) == 0 {
		return cfg, nil
	}

	out := &kubeconfig.Config{Sources: cfg.Sources}
	for _, c := range cfg.Contexts {
		if want[c.Name] {
			out.Contexts = append(out.Contexts, c)
		}
	}
	// Keep a current context only if it survived the filter, so connect order
	// still favours the developer's usual workspace when it is included.
	if want[cfg.Current] {
		out.Current = cfg.Current
	} else {
		out.Current = order[0]
	}
	return out, nil
}

// gather connects and fetches every context concurrently, so one cluster
// behind a VPN that is off cannot delay the rest.
func gather(ctx context.Context, mgr *clusters.Manager, cfg *kubeconfig.Config, timeout time.Duration) map[string]result {
	var (
		mu  sync.Mutex
		out = map[string]result{}
		wg  sync.WaitGroup
	)

	for _, kctx := range cfg.Contexts {
		wg.Add(1)
		go func(kctx kubeconfig.Context) {
			defer wg.Done()

			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			var r result
			if r.err = mgr.Connect(cctx, kctx.Name); r.err == nil {
				if client, ok := mgr.ClientFor(kctx.Name); ok {
					r.snap, r.err = clusters.Fetch(cctx, client, kctx)
				}
			}

			mu.Lock()
			out[kctx.Name] = r
			mu.Unlock()
		}(kctx)
	}

	wg.Wait()
	return out
}

func printContext(out io.Writer, mgr *clusters.Manager, kctx kubeconfig.Context, r result) {
	name := kctx.Name
	status, _ := mgr.Status(name)

	fmt.Fprintf(out, "── %s  [%s]", name, status.State)
	if status.State == clusters.StateStale && status.Age > 0 {
		fmt.Fprintf(out, "  snapshot %s old", status.Age.Round(time.Second))
	}
	fmt.Fprintln(out)

	// An unreached cluster is never printed as an empty app list. Absence of
	// knowledge and absence of apps are different facts.
	if !status.State.HasData() {
		if r.err != nil {
			fmt.Fprintf(out, "   nothing known: %v\n", r.err)
		} else {
			fmt.Fprintln(out, "   nothing known yet")
		}
		if status.State == clusters.StateUnauthorized {
			printCredentialHelp(out, kctx, r.err)
		}
		fmt.Fprintln(out)
		return
	}
	if r.err != nil {
		fmt.Fprintf(out, "   fetch failed: %v\n\n", r.err)
		return
	}

	fmt.Fprintf(out, "   scope: %s", r.snap.Scope)
	if r.snap.Scope.Reason != "" {
		fmt.Fprintf(out, " (%s)", r.snap.Scope.Reason)
	}
	fmt.Fprintln(out)
	if len(r.snap.Partial) > 0 {
		fmt.Fprintf(out, "   unreadable kinds: %s\n", strings.Join(r.snap.Partial, ", "))
	}

	if len(r.snap.Apps) == 0 {
		fmt.Fprintln(out, "   no apps in scope")
		fmt.Fprintln(out)
		return
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   NAMESPACE\tAPP\tKIND\tOBJECTS\tGROUPED BY")
	rows := append([]apps.App(nil), r.snap.Apps...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Key.Namespace != rows[j].Key.Namespace {
			return rows[i].Key.Namespace < rows[j].Key.Namespace
		}
		return rows[i].Key.Name < rows[j].Key.Name
	})
	for _, a := range rows {
		fmt.Fprintf(tw, "   %s\t%s\t%s\t%d\t%s\n",
			a.Key.Namespace, a.Key.Name, a.Kind, len(a.Workloads), a.Origin)
	}
	tw.Flush()

	fmt.Fprintf(out, "\n   %d apps\n", len(r.snap.Apps))
	printOriginTally(out, r.snap.Apps)
	fmt.Fprintln(out)
}

// printOriginTally is the diagnostic that matters for the kill criterion.
// A cluster grouping mostly by workload-name means the precedence chain is
// earning nothing there, which is a finding in itself.
func printOriginTally(out io.Writer, list []apps.App) {
	tally := map[string]int{}
	for _, a := range list {
		tally[a.Origin.String()]++
	}
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, tally[k]))
	}
	fmt.Fprintf(out, "   grouped by: %s\n", strings.Join(parts, ", "))
}

// printCredentialHelp turns a rejected credential into an actionable next step.
//
// Reporting "unauthorized" and stopping leaves the developer to remember which
// of several login commands applies to this cluster. The kubeconfig already
// records how the context authenticates, so name it.
func printCredentialHelp(out io.Writer, kctx kubeconfig.Context, cause error) {
	if kctx.Exec == nil {
		fmt.Fprintln(out, "   this context does not use a credential plugin; check its token or client certificate")
		return
	}

	// A missing binary and an expired session both surface as a credential
	// failure, but the fixes are unrelated. Telling someone to run a login
	// command for a tool they have not installed wastes their time.
	if isMissingExecutable(cause) {
		fmt.Fprintf(out, "   the credential plugin is not installed or not on PATH: %s\n", kctx.Exec.Command)
		fmt.Fprintln(out, "   install it, or fix the command in your kubeconfig, then re-run kubeside")
		return
	}

	if hint := kctx.Exec.LoginHint(); hint != "" {
		fmt.Fprintln(out, "   your session for this context has likely expired")
		fmt.Fprintf(out, "   run: %s\n", hint)
		fmt.Fprintln(out, "   then re-run kubeside")
		return
	}
	// No known mapping. Print what the kubeconfig runs rather than guessing a
	// login command that may not exist.
	fmt.Fprintf(out, "   this context authenticates with: %s\n", kctx.Exec.Describe())
	fmt.Fprintln(out, "   re-authenticate with that tool, then re-run kubeside")
}

// isMissingExecutable reports whether a credential failure was caused by the
// plugin binary being absent rather than by a rejected or expired credential.
func isMissingExecutable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

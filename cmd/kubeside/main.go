// Command kubeside is a Kubernetes client scoped to the application developer.
//
// This is the M0 spike: it prints an app list to the terminal so a human can
// judge whether grouping produces apps they recognise. No UI until the apps
// feature. kubeside writes nothing to disk.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
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

	mgr := clusters.New(cfg, clusters.KubeConnector{Opts: opts}, clusters.Options{})
	defer mgr.Close()

	fmt.Fprintf(out, "kubeside %s\n", version.String())
	fmt.Fprintf(out, "%d contexts from %s\n\n", len(cfg.Contexts), strings.Join(cfg.Sources, ", "))

	results := gather(context.Background(), mgr, cfg, *timeout)
	for _, name := range mgr.ConnectOrder() {
		printContext(out, mgr, name, results[name])
	}
	return nil
}

type result struct {
	snap clusters.Snapshot
	err  error
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

func printContext(out io.Writer, mgr *clusters.Manager, name string, r result) {
	status, _ := mgr.Status(name)

	header := name
	if cur, ok := mgr.Status(name); ok && cur.State == clusters.StateLive {
		header = name
	}
	fmt.Fprintf(out, "── %s  [%s]", header, status.State)
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

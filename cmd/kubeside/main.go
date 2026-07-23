// Command kubeside is a Kubernetes client scoped to the application developer.
//
// Local mode starts a server on 127.0.0.1 and opens a browser. Serve mode runs
// the same binary in-cluster. kubeside writes nothing to disk: history is
// reconstructed from the cluster and buffered in memory for the session only.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dynaum/kubeside/internal/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "kubeside:", err)
		os.Exit(1)
	}
}

// run is separated from main so tests can drive the CLI without exiting.
func run(args []string, out *os.File) error {
	fs := flag.NewFlagSet("kubeside", flag.ContinueOnError)
	fs.SetOutput(out)
	showVersion := fs.Bool("version", false, "print version and exit")
	serve := fs.Bool("serve", false, "run in-cluster as a team web UI")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Fprintln(out, version.String())
		return nil
	}

	if *serve {
		return fmt.Errorf("--serve is not implemented before M5; see docs/06-roadmap.md")
	}

	// M0 prints an app list to the terminal. No UI until M1.
	fmt.Fprintln(out, "kubeside", version.String())
	fmt.Fprintln(out, "no clusters wired yet: see issue #2 (ClusterManager)")
	return nil
}

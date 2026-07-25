package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/dynaum/kubeside/internal/api"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/web"
)

// serveUI starts the local web server and opens the browser at the URL that
// already carries the session token.
func serveUI(out io.Writer, cfg *kubeconfig.Config, mgr *clusters.Manager, opts kubeconfig.Options, conf *config.Config, timeout time.Duration, port int, open bool) error {
	svc := api.NewService(cfg, mgr, opts, conf, timeout)
	// No tunnel outlives the process that opened it.
	defer svc.Close()

	// Idle connections are dropped while the server runs, so a laptop that
	// opened six environments this morning is not still watching six.
	janitor, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()
	svc.StartJanitor(janitor)

	ui, built := web.FS()
	if !built {
		fmt.Fprintln(out, "note: the web UI is not built into this binary.")
		fmt.Fprintln(out, "      build it with: npm --prefix web ci && npm --prefix web run build")
		fmt.Fprintln(out, "      then rebuild kubeside. Serving the placeholder for now.")
	}

	srv, err := api.New(svc, http.FileServerFS(ui))
	if err != nil {
		return err
	}

	l, err := api.Listen(port)
	if err != nil {
		return fmt.Errorf("bind 127.0.0.1:%d: %w", port, err)
	}

	url := srv.URL(l)
	fmt.Fprintf(out, "kubeside serving on %s\n", url)
	fmt.Fprintln(out, "reads your kubeconfig, writes nothing to disk. Ctrl-C to stop.")

	if open {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(out, "could not open a browser (%v); open the URL above yourself\n", err)
		}
	}

	// http.Serve blocks until the process is interrupted.
	return http.Serve(l, srv.Handler())
}

// openBrowser opens the default browser at url, best-effort.
func openBrowser(url string) error {
	cmd, args := browserCommand(runtime.GOOS, url)
	return exec.Command(cmd, args...).Start()
}

// browserCommand is how each platform opens a URL.
//
// Split out from the call so all three are testable from one machine. A launch
// that silently does nothing on the third platform is the kind of thing nobody
// notices until somebody on Windows reports a blank terminal.
//
// Windows uses rundll32 rather than "cmd /c start" because start treats its
// first quoted argument as a window title and mangles a URL carrying a query
// string, which is exactly the URL kubeside opens.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	}
	return "xdg-open", []string{url}
}

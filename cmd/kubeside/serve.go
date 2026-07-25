package main

import (
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
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	return exec.Command(cmd, append(args, url)...).Start()
}

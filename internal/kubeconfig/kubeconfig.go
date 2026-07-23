// Package kubeconfig reads the developer's existing kubeconfig.
//
// There is no import step and no separate credential store: if kubectl works,
// kubeside works. The file is read-only input. kubeside never writes to it,
// never copies credentials out of it, and loads every context rather than only
// current-context, because multi-environment is the point of the product.
package kubeconfig

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Context is one kubeconfig context, flattened to what kubeside needs.
type Context struct {
	Name      string // context name, personal and rename-prone
	Cluster   string // cluster nickname within the kubeconfig
	Server    string // API server URL; stable across renames, so shared config keys off it
	User      string
	Namespace string // per-context default, seeds the namespace filter
	IsCurrent bool
	Insecure  bool // insecure-skip-tls-verify, surfaced as a warning in the UI

	// Exec describes the credential plugin this context authenticates with,
	// when it uses one. Captured so that a rejected credential can name the
	// command that would fix it instead of printing a bare error.
	Exec *ExecConfig
}

// ExecConfig is the kubeconfig exec block for a context.
type ExecConfig struct {
	Command string
	Args    []string
	Env     map[string]string
}

// LoginHint returns the command a developer should run when this context's
// credentials are rejected, or "" when nothing useful can be said.
//
// It never invents a command that is not implied by the kubeconfig. Guessing
// wrong here is worse than staying quiet: it sends someone to run something
// that cannot help.
func (e *ExecConfig) LoginHint() string {
	if e == nil || e.Command == "" {
		return ""
	}
	base := filepath.Base(e.Command)

	switch {
	case base == "aws" && hasArg(e.Args, "eks"):
		if p := e.awsProfile(); p != "" {
			return "aws sso login --profile " + p
		}
		return "aws sso login"
	case strings.Contains(base, "gke-gcloud-auth-plugin"), base == "gcloud":
		return "gcloud auth login"
	case base == "az", strings.Contains(base, "azure"):
		return "az login"
	case base == "tsh":
		return "tsh login"
	case strings.Contains(base, "kubelogin"), strings.Contains(base, "oidc-login"), strings.Contains(base, "oidc_login"):
		return "kubelogin clear-token-cache, then re-run"
	}
	return ""
}

// Describe renders the configured credential command, used when no specific
// login hint is known so the developer at least sees what ran.
func (e *ExecConfig) Describe() string {
	if e == nil || e.Command == "" {
		return ""
	}
	if len(e.Args) == 0 {
		return e.Command
	}
	return e.Command + " " + strings.Join(e.Args, " ")
}

// awsProfile finds the profile this context authenticates with, from the exec
// args or the exec env block.
func (e *ExecConfig) awsProfile() string {
	for i, a := range e.Args {
		if a == "--profile" && i+1 < len(e.Args) {
			return e.Args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--profile="); ok {
			return v
		}
	}
	return e.Env["AWS_PROFILE"]
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// Config is the merged view of every kubeconfig file in the chain.
type Config struct {
	Contexts []Context
	Current  string
	Sources  []string // files merged, in precedence order
}

// Options selects which files to read. Zero value follows kubectl: the
// KUBECONFIG environment chain, falling back to ~/.kube/config.
type Options struct {
	// ExplicitPath is the --kubeconfig flag. Highest precedence.
	ExplicitPath string
	// Precedence overrides the KUBECONFIG chain. Tests set this directly;
	// production leaves it nil so clientcmd reads the environment.
	Precedence []string
}

// Load reads and merges the kubeconfig chain.
//
// Exec credential plugins are deliberately not invoked here. Loading must stay
// cheap and side-effect free; plugins run later, per context, when a
// connection is actually made.
func Load(opts Options) (*Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.ExplicitPath != "" {
		rules.ExplicitPath = opts.ExplicitPath
	}
	if opts.Precedence != nil {
		rules.Precedence = opts.Precedence
	}

	raw, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return fromAPIConfig(raw, sources(rules)), nil
}

func sources(rules *clientcmd.ClientConfigLoadingRules) []string {
	if rules.ExplicitPath != "" {
		return []string{rules.ExplicitPath}
	}
	return append([]string(nil), rules.Precedence...)
}

func fromAPIConfig(raw *clientcmdapi.Config, srcs []string) *Config {
	cfg := &Config{Current: raw.CurrentContext, Sources: srcs}

	for name, kctx := range raw.Contexts {
		c := Context{
			Name:      name,
			Cluster:   kctx.Cluster,
			User:      kctx.AuthInfo,
			Namespace: kctx.Namespace,
			IsCurrent: name == raw.CurrentContext,
		}
		if cl, ok := raw.Clusters[kctx.Cluster]; ok {
			c.Server = cl.Server
			c.Insecure = cl.InsecureSkipTLSVerify
		}
		if au, ok := raw.AuthInfos[kctx.AuthInfo]; ok && au.Exec != nil {
			ec := &ExecConfig{
				Command: au.Exec.Command,
				Args:    append([]string(nil), au.Exec.Args...),
			}
			if len(au.Exec.Env) > 0 {
				ec.Env = make(map[string]string, len(au.Exec.Env))
				for _, kv := range au.Exec.Env {
					ec.Env[kv.Name] = kv.Value
				}
			}
			c.Exec = ec
		}
		cfg.Contexts = append(cfg.Contexts, c)
	}

	// Map iteration is random; the app list must not reorder between runs.
	sort.Slice(cfg.Contexts, func(i, j int) bool {
		return cfg.Contexts[i].Name < cfg.Contexts[j].Name
	})
	return cfg
}

// Get returns the named context.
func (c *Config) Get(name string) (Context, bool) {
	for _, ctx := range c.Contexts {
		if ctx.Name == name {
			return ctx, true
		}
	}
	return Context{}, false
}

// CurrentContext returns the context kubectl would use. It connects first, so
// the developer's usual workspace renders while other clusters handshake.
func (c *Config) CurrentContext() (Context, bool) {
	if c.Current == "" {
		return Context{}, false
	}
	return c.Get(c.Current)
}

// RESTConfigFor builds a client config for one context.
//
// This is where exec credential plugins (aws eks get-token,
// gke-gcloud-auth-plugin, kubelogin, tsh) become live: clientcmd runs them as
// native child processes with the real environment, which is why kubeside is
// not affected by the sandboxing failures that plague packaged competitors.
func RESTConfigFor(opts Options, contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.ExplicitPath != "" {
		rules.ExplicitPath = opts.ExplicitPath
	}
	if opts.Precedence != nil {
		rules.Precedence = opts.Precedence
	}

	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("rest config for context %q: %w", contextName, err)
	}
	return rc, nil
}

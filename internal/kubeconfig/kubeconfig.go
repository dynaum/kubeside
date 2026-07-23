// Package kubeconfig reads the developer's existing kubeconfig.
//
// There is no import step and no separate credential store: if kubectl works,
// kubeside works. The file is read-only input. kubeside never writes to it,
// never copies credentials out of it, and loads every context rather than only
// current-context, because multi-environment is the point of the product.
package kubeconfig

import (
	"fmt"
	"sort"

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

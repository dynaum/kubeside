// Package config loads the optional kubeside config file.
//
// Zero-config is the first run: kubeside infers environments from context names
// and never writes what it inferred. A file exists only because the user made
// one, and this package only ever reads it.
//
// Two rules shape everything here. A shared file identifies clusters by API
// server URL, because context names are personal: one teammate's
// "prod-us-east" is another's EKS ARN. And anything the file gets wrong is an
// error that names the offending value, never a silent fallback. A misspelled
// key that quietly does nothing would leave a developer believing prod is
// guarded when it is not.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dynaum/kubeside/internal/environments"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"sigs.k8s.io/yaml"
)

// EnvVar points at a shared config file, typically one a team commits.
const EnvVar = "KUBESIDE_CONFIG"

// Options controls where the config is read from.
type Options struct {
	// Path overrides discovery. A path that does not exist is an error: the
	// caller asked for that file specifically.
	Path string
}

// file mirrors the YAML document. It exists only to be decoded and validated
// into Config, which is what the rest of the program uses.
type file struct {
	Environments  []envEntry          `json:"environments"`
	Apps          map[string]appMatch `json:"apps"`
	Defaults      *defaultsEntry      `json:"defaults"`
	NamespaceAxis *axisEntry          `json:"namespaceAxis"`
}

type envEntry struct {
	Name     string   `json:"name"`
	Risk     string   `json:"risk"`
	Color    string   `json:"color"`
	Write    string   `json:"write"`
	Contexts []string `json:"contexts"`
	Clusters []string `json:"clusters"`
}

type appMatch struct {
	Match map[string]string `json:"match"`
}

type defaultsEntry struct {
	Namespaces []string `json:"namespaces"`
	Metrics    string   `json:"metrics"`
}

type axisEntry struct {
	Pattern      string   `json:"pattern"`
	Systems      []string `json:"systems"`
	Environments []string `json:"environments"`
}

// Defaults are the fallbacks the file may set.
type Defaults struct {
	// Namespaces is the list to read when cluster-scoped discovery is refused.
	// Empty means probe: try listing, then the context's own namespace.
	Namespaces []string
	// Metrics selects the usage source: auto, metrics-server, prometheus, none.
	Metrics string
}

// Ref is one app in one environment, when the name or namespace differs.
type Ref struct {
	Namespace string
	Name      string
}

// Config is the loaded, validated file.
type Config struct {
	// Path is where it came from, empty when no file exists.
	Path string
	// Environments are the configured ones, in the order written, so the UI can
	// list them the way the team thinks of them.
	Environments []environments.Environment
	// Axis decomposes namespaces when one cluster holds several environments.
	// Nil when the file configured none.
	Axis *environments.Resolver
	// AxisConfig is what Axis was built from, kept so the UI can explain it.
	AxisConfig environments.Config
	Defaults   Defaults

	byContext map[string]int
	byCluster map[string]int
	apps      map[string]map[string]Ref
}

var (
	validRisk    = map[string]environments.Risk{"low": environments.RiskLow, "medium": environments.RiskMedium, "high": environments.RiskHigh}
	validWrite   = map[string]environments.WritePolicy{"allow": environments.WriteAllow, "confirm": environments.WriteConfirm, "deny": environments.WriteDeny, "break-glass": environments.WriteBreakGlass}
	validMetrics = map[string]bool{"auto": true, "metrics-server": true, "prometheus": true, "none": true}
)

// Load reads the config, or returns an empty one when no file exists.
//
// Discovery order: Options.Path, then KUBESIDE_CONFIG, then
// $XDG_CONFIG_HOME/kubeside/config.yaml, then ~/.config/kubeside/config.yaml.
// Nothing is created along the way; a missing file at the end of that list is
// the normal first run, not a failure.
func Load(opts Options) (*Config, error) {
	c := Empty()

	path, explicit := locate(opts)
	if path == "" {
		return c, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return c, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c.Path = path

	var f file
	// Strict: an unknown key is a typo, and a typo that does nothing is worse
	// than one that fails.
	if err := yaml.UnmarshalStrict(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.apply(f, path); err != nil {
		return nil, err
	}
	return c, nil
}

// locate finds the file to read and reports whether the user named it
// explicitly, which is what makes a missing file an error rather than a
// first run.
func locate(opts Options) (path string, explicit bool) {
	if opts.Path != "" {
		return opts.Path, true
	}
	if p := strings.TrimSpace(os.Getenv(EnvVar)); p != "" {
		return p, true
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "kubeside", "config.yaml"), false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".config", "kubeside", "config.yaml"), false
}

func (c *Config) apply(f file, path string) error {
	for i, e := range f.Environments {
		env, err := resolveEnv(e, path)
		if err != nil {
			return err
		}
		c.Environments = append(c.Environments, env)

		for _, name := range e.Contexts {
			if prev, dup := c.byContext[name]; dup {
				return fmt.Errorf("%s: context %q is bound to both %q and %q; one context is one environment",
					path, name, c.Environments[prev].Name, env.Name)
			}
			c.byContext[name] = i
		}
		for _, u := range e.Clusters {
			k := NormalizeURL(u)
			if prev, dup := c.byCluster[k]; dup {
				return fmt.Errorf("%s: cluster %q is bound to both %q and %q",
					path, u, c.Environments[prev].Name, env.Name)
			}
			c.byCluster[k] = i
		}
	}

	for app, m := range f.Apps {
		for env, ref := range m.Match {
			ns, name, ok := strings.Cut(ref, "/")
			if !ok || ns == "" || name == "" {
				return fmt.Errorf("%s: apps.%s.match.%s is %q; it must be namespace/name", path, app, env, ref)
			}
			if c.apps[app] == nil {
				c.apps[app] = map[string]Ref{}
			}
			c.apps[app][env] = Ref{Namespace: ns, Name: name}
		}
	}

	if d := f.Defaults; d != nil {
		c.Defaults.Namespaces = d.Namespaces
		if d.Metrics != "" {
			if !validMetrics[d.Metrics] {
				return fmt.Errorf("%s: defaults.metrics is %q; use auto, metrics-server, prometheus, or none", path, d.Metrics)
			}
			c.Defaults.Metrics = d.Metrics
		}
	}

	if a := f.NamespaceAxis; a != nil {
		cfg := environments.Config{Pattern: a.Pattern, Systems: a.Systems, Environments: a.Environments}
		r, err := environments.NewResolver(cfg)
		if err != nil {
			return fmt.Errorf("%s: namespaceAxis: %w", path, err)
		}
		c.Axis, c.AxisConfig = r, cfg
	}
	return nil
}

// resolveEnv turns one entry into an Environment.
//
// Unset fields inherit the classification of the environment's own name, so a
// file that only binds contexts keeps the guardrails the name implies. An
// unrecognized name inherits prod-strength guardrails, exactly as an
// unclassified context does.
func resolveEnv(e envEntry, path string) (environments.Environment, error) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return environments.Environment{}, fmt.Errorf("%s: an environment has no name", path)
	}

	env := environments.Classify(name)

	if e.Risk != "" {
		r, ok := validRisk[e.Risk]
		if !ok {
			return environments.Environment{}, fmt.Errorf("%s: environment %q has risk %q; use low, medium, or high", path, name, e.Risk)
		}
		env.Risk = r
		env.Hazard = r == environments.RiskHigh
		// Risk without an explicit write policy re-derives the policy, so
		// raising the risk of an environment also tightens its writes.
		if e.Write == "" {
			env.Write = writeForRisk(r)
		}
	}
	if e.Write != "" {
		w, ok := validWrite[e.Write]
		if !ok {
			return environments.Environment{}, fmt.Errorf("%s: environment %q has write %q; use allow, confirm, deny, or break-glass", path, name, e.Write)
		}
		env.Write = w
	}
	if e.Color != "" {
		env.Color = e.Color
	}
	return env, nil
}

// writeForRisk is the default gate for a risk level. It matches the inferred
// tiers, so configuring risk alone behaves like naming the tier.
func writeForRisk(r environments.Risk) environments.WritePolicy {
	switch r {
	case environments.RiskLow:
		return environments.WriteAllow
	case environments.RiskMedium:
		return environments.WriteConfirm
	}
	return environments.WriteDeny
}

// Environment resolves one kubeconfig context.
//
// Cluster URL wins over context name, since it survives every rename and every
// personal naming convention. A context the file says nothing about still
// classifies by name, so configuring one environment does not blind kubeside to
// the others.
func (c *Config) Environment(kctx kubeconfig.Context) environments.Environment {
	if i, ok := c.byCluster[NormalizeURL(kctx.Server)]; ok {
		return c.Environments[i]
	}
	if i, ok := c.byContext[kctx.Name]; ok {
		return c.Environments[i]
	}
	return environments.Classify(kctx.Name)
}

// AppRef returns the configured identity of an app in one environment, when the
// file overrode it.
func (c *Config) AppRef(app, env string) (Ref, bool) {
	r, ok := c.apps[app][env]
	return r, ok
}

// NormalizeURL makes server URLs comparable across the small differences that
// mean nothing: a trailing slash, or a host written in a different case.
func NormalizeURL(u string) string {
	s := strings.TrimSpace(u)
	if s == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(s, "/"))
}

// Empty is the zero-config state: no file, everything inferred. Callers that
// have no config still get a usable *Config rather than a nil check at every
// call site.
func Empty() *Config {
	return &Config{
		Defaults:  Defaults{Metrics: "auto"},
		byContext: map[string]int{},
		byCluster: map[string]int{},
		apps:      map[string]map[string]Ref{},
	}
}

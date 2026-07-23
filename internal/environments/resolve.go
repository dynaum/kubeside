package environments

import "sort"

// Segment names the resolver treats specially. A template is free to use other
// names, but these three drive the environment axis and app identity.
const (
	SegEnv     = "env"
	SegSystem  = "system"
	SegService = "service"
)

// Decomposition is what the resolver made of one namespace.
type Decomposition struct {
	// Tenant is true when the namespace matched the pattern and any
	// constraints. A platform namespace (kube-system, argocd, a vendor
	// component) is not a tenant and has no environment.
	Tenant bool

	Environment string
	System      string
	Service     string

	// Segments holds every captured placeholder, so a template with names
	// beyond env/system/service still exposes them.
	Segments map[string]string
}

// Identity is how an app is matched across environments: everything except the
// environment segment. Two namespaces that agree on identity but differ on
// environment are the same service in two places.
func (d Decomposition) Identity() string {
	if !d.Tenant {
		return ""
	}
	if d.System != "" {
		return d.System + "/" + d.Service
	}
	return d.Service
}

// Resolver decomposes namespaces and decides tenant versus platform.
type Resolver struct {
	pattern *Pattern
	// systems, when non-empty, constrains the system segment to a known set.
	// This is the anchor that keeps platform namespaces out: a structural rule
	// alone matches "aws-load-balancer-controller" as a tenant service.
	systems map[string]bool
	// envs, when non-empty, constrains the environment segment instead of (or
	// in addition to) systems. Either anchor is sufficient.
	envs map[string]bool
}

// Config configures a Resolver.
type Config struct {
	// Pattern is the namespace template, e.g. "{env}-{system}-{service}".
	Pattern string
	// Systems anchors the system segment. Recommended when environments are
	// dynamic, since new environments then need no reconfiguration.
	Systems []string
	// Environments anchors the environment segment. Use when systems vary but
	// the environment set is stable.
	Environments []string
}

// NewResolver builds a resolver from config.
func NewResolver(cfg Config) (*Resolver, error) {
	p, err := ParsePattern(cfg.Pattern)
	if err != nil {
		return nil, err
	}
	r := &Resolver{pattern: p, systems: toSet(cfg.Systems), envs: toSet(cfg.Environments)}
	return r, nil
}

// Decompose classifies one namespace.
func (r *Resolver) Decompose(namespace string) Decomposition {
	seg, ok := r.pattern.Match(namespace)
	if !ok {
		return Decomposition{}
	}

	env := seg[SegEnv]
	system := seg[SegSystem]

	// An anchor, when configured, must agree. Without any anchor the resolver
	// trusts the structure, which over-matches; callers with platform
	// namespaces should configure one.
	if len(r.systems) > 0 && !r.systems[system] {
		return Decomposition{}
	}
	if len(r.envs) > 0 && !r.envs[env] {
		return Decomposition{}
	}

	return Decomposition{
		Tenant:      true,
		Environment: env,
		System:      system,
		Service:     seg[SegService],
		Segments:    seg,
	}
}

// EnvironmentCandidate is a proposed environment, with evidence.
type EnvironmentCandidate struct {
	Name     string
	Services int // distinct identities seen under this environment segment
}

// InferEnvironments proposes environment names from a set of namespaces.
//
// It is a suggestion, never applied automatically. Structurally, "team-a-*"
// and "team-b-*" are indistinguishable from environment prefixes while meaning
// something entirely different, so a human confirms before these become an
// axis. The ranking by shared-service count is the signal: real environments
// run largely the same set of services, teams do not.
func (r *Resolver) InferEnvironments(namespaces []string) []EnvironmentCandidate {
	byEnv := map[string]map[string]bool{}
	for _, ns := range namespaces {
		seg, ok := r.pattern.Match(ns)
		if !ok {
			continue
		}
		if len(r.systems) > 0 && !r.systems[seg[SegSystem]] {
			continue
		}
		env := seg[SegEnv]
		if byEnv[env] == nil {
			byEnv[env] = map[string]bool{}
		}
		id := seg[SegSystem] + "/" + seg[SegService]
		byEnv[env][id] = true
	}

	out := make([]EnvironmentCandidate, 0, len(byEnv))
	for env, ids := range byEnv {
		out = append(out, EnvironmentCandidate{Name: env, Services: len(ids)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Services != out[j].Services {
			return out[i].Services > out[j].Services
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func toSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

package environments

import "strings"

// Risk drives how much friction a write requires. Unknown risk is treated as
// high, because assuming an unfamiliar environment is safe is the dangerous
// default.
type Risk int

const (
	RiskUnknown Risk = iota
	RiskLow
	RiskMedium
	RiskHigh
)

func (r Risk) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	}
	return "high" // unknown collapses to high; never render an unknown as safe
}

// WritePolicy is how destructive actions are gated in an environment.
type WritePolicy int

const (
	// WriteDeny blocks writes. The default for anything not proven safe.
	WriteDeny WritePolicy = iota
	// WriteConfirm allows writes after a typed confirmation.
	WriteConfirm
	// WriteAllow permits writes directly.
	WriteAllow
	// WriteBreakGlass unlocks writes for a limited window with a stated reason.
	WriteBreakGlass
)

func (w WritePolicy) String() string {
	switch w {
	case WriteConfirm:
		return "confirm"
	case WriteAllow:
		return "allow"
	case WriteBreakGlass:
		return "break-glass"
	}
	return "deny"
}

// Environment is a named tier with a risk level and a write policy.
type Environment struct {
	Name   string
	Risk   Risk
	Write  WritePolicy
	Color  string
	Hazard bool // render hazard hatching, high-risk environments only
}

// tierRule matches a name to a tier by keyword. Order matters: production is
// checked before staging, so "prod-staging-mirror" reads as the more dangerous
// tier rather than the less.
type tierRule struct {
	keywords map[string]bool
	risk     Risk
	write    WritePolicy
	color    string
}

var tierRules = []tierRule{
	{set("prod", "production", "prd", "live"), RiskHigh, WriteDeny, "red"},
	{set("stg", "staging", "stage", "uat", "preprod"), RiskMedium, WriteConfirm, "amber"},
	{set("qa", "test", "dev", "develop", "sandbox", "sbx"), RiskLow, WriteAllow, "green"},
}

// Classify infers an environment from a name.
//
// The name is split into tokens on common separators and each token's trailing
// digits are stripped, so "qa1" and "sandbox3" classify the same as "qa" and
// "sandbox". That matters because real environments are frequently numbered.
//
// An unmatched name becomes unclassified and inherits prod-strength
// guardrails: high risk, writes denied, hazard on. Treating the unknown as
// dangerous is the safe direction to be wrong in.
func Classify(name string) Environment {
	tokens := tokenize(name)
	for _, rule := range tierRules {
		for _, tok := range tokens {
			if rule.keywords[tok] {
				return Environment{
					Name:   name,
					Risk:   rule.risk,
					Write:  rule.write,
					Color:  rule.color,
					Hazard: rule.risk == RiskHigh,
				}
			}
		}
	}
	return Environment{
		Name:   name,
		Risk:   RiskHigh,
		Write:  WriteDeny,
		Color:  "violet",
		Hazard: true,
	}
}

// tokenize splits a name on separators and strips trailing digits from each
// token, so "prod-us-east-2" yields prod, us, east.
func tokenize(name string) []string {
	fields := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' ' || r == '/'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.TrimRight(f, "0123456789"))
	}
	return out
}

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// WithName returns a copy classified as name, preserving nothing else. It is a
// convenience for turning an inferred environment segment into an Environment.
func WithName(name string) Environment { return Classify(name) }

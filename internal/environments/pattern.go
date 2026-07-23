// Package environments resolves the environment axis from namespace names.
//
// The multi-cluster model assumed environment maps to a kubeconfig context.
// A real cluster disproved that: it held several environments side by side,
// distinguished by a namespace prefix, sharing one context and one set of
// credentials (see docs/04-multi-cluster.md and issue #39). This package makes
// environment a dimension that can come from a namespace pattern.
package environments

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern decomposes a namespace name into named segments from a template such
// as "{env}-{system}-{service}".
//
// Two rules make the common conventions work:
//
//   - A non-final placeholder matches one separator-free segment. The final
//     placeholder captures the remainder, because service names contain the
//     separator ("notification-aws-ses-adapter" is one service, not three).
//   - A template segment can be a literal instead of a placeholder. Anchoring
//     one segment is what separates real tenant namespaces from platform ones
//     that happen to share the same shape: a structural rule alone matches
//     "aws-load-balancer-controller" as if it were a tenant service, which it
//     is not.
type Pattern struct {
	raw   string
	re    *regexp.Regexp
	names []string // placeholder names in order
}

// placeholderRE finds {name} tokens in a template.
var placeholderRE = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// ParsePattern compiles a namespace template.
//
// The template must contain at least one placeholder, and placeholder names
// must be unique so a decomposition is unambiguous.
func ParsePattern(template string) (*Pattern, error) {
	if strings.TrimSpace(template) == "" {
		return nil, fmt.Errorf("empty namespace pattern")
	}

	// Walk the template, emitting a regex with a capture per placeholder and
	// escaped literals between them.
	var (
		b        strings.Builder
		names    []string
		seen     = map[string]bool{}
		last     = 0
		idxs     = placeholderRE.FindAllStringSubmatchIndex(template, -1)
		nHolders = len(idxs)
	)
	if nHolders == 0 {
		return nil, fmt.Errorf("pattern %q has no {placeholder}", template)
	}

	b.WriteString("^")
	for i, m := range idxs {
		start, end := m[0], m[1]
		name := template[m[2]:m[3]]
		if seen[name] {
			return nil, fmt.Errorf("pattern %q repeats placeholder {%s}", template, name)
		}
		seen[name] = true

		b.WriteString(regexp.QuoteMeta(template[last:start]))
		if i == nHolders-1 {
			// Final placeholder captures the rest, so multi-segment service
			// names stay intact.
			b.WriteString("(.+)")
		} else {
			// Non-final placeholder is minimal, so it takes one segment.
			b.WriteString("(.+?)")
		}
		names = append(names, name)
		last = end
	}
	b.WriteString(regexp.QuoteMeta(template[last:]))
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("compile pattern %q: %w", template, err)
	}
	return &Pattern{raw: template, re: re, names: names}, nil
}

// Match decomposes a namespace, returning the segment values keyed by
// placeholder name. The bool is false when the namespace does not fit, which
// is the normal answer for a platform namespace.
func (p *Pattern) Match(namespace string) (map[string]string, bool) {
	m := p.re.FindStringSubmatch(namespace)
	if m == nil {
		return nil, false
	}
	out := make(map[string]string, len(p.names))
	for i, name := range p.names {
		out[name] = m[i+1]
	}
	return out, true
}

// Names returns the placeholder names in template order.
func (p *Pattern) Names() []string { return append([]string(nil), p.names...) }

// String returns the original template.
func (p *Pattern) String() string { return p.raw }

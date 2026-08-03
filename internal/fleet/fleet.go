// Package fleet answers "is every cluster running the latest version".
//
// One app, one row per cluster, environment demoted to a column. The promotion
// matrix compares environments side by side; this compares the clusters inside
// and across them, which is the question a team running prod in three regions
// asks and the matrix cannot phrase.
//
// Two facts carry the screen: the newest tag among the clusters that answered,
// and every cluster behind it. Everything else is there so a cluster that did
// not answer is never mistaken for a cluster without the app.
package fleet

import (
	"fmt"
	"sort"

	"github.com/dynaum/kubeside/internal/promotion"
)

// Placement states.
const (
	StatePresent     = "present"
	StateAbsent      = "absent"
	StateDenied      = "denied"
	StateUnreachable = "unreachable"
	StatePending     = "pending"
)

// Placement is one app as it exists in one cluster.
type Placement struct {
	Context   string `json:"context"`
	ClusterID string `json:"clusterId"`
	Env       string `json:"env"`
	Namespace string `json:"namespace,omitempty"`

	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`

	Image  string `json:"image,omitempty"`
	Tag    string `json:"tag,omitempty"`
	Digest string `json:"digest,omitempty"`
	// DigestPending marks a placement whose digest has not arrived. A missing
	// digest never reads as a match.
	DigestPending bool `json:"digestPending,omitempty"`

	Health     string `json:"health,omitempty"`
	Ready      string `json:"ready,omitempty"`
	RevisionAt string `json:"revisionAt,omitempty"`
	// Aliases names other kubeconfig contexts pointing at this same cluster,
	// so a merged row says what it merged.
	Aliases []string `json:"aliases,omitempty"`
}

// Row is one cluster, judged against the newest version.
type Row struct {
	Placement
	Behind bool `json:"behind,omitempty"`
	// MutableTag marks a cluster carrying the newest tag with a digest another
	// cluster's copy of that tag does not share.
	MutableTag bool   `json:"mutableTag,omitempty"`
	Note       string `json:"note,omitempty"`
}

// View is the whole screen.
type View struct {
	App       string `json:"app"`
	Namespace string `json:"namespace"`
	Rows      []Row  `json:"rows"`

	Newest   string `json:"newest,omitempty"`
	Clusters int    `json:"clusters"`
	Present  int    `json:"present"`
	Behind   int    `json:"behind"`
	// MutableTag is true when one tag resolved to more than one digest. It
	// outranks being behind: a cluster openly on an older version is a
	// schedule, two clusters claiming one version while running different
	// code is a defect.
	MutableTag bool `json:"mutableTag,omitempty"`
}

// Build derives the screen from one placement per context.
func Build(app, namespace string, ps []Placement) View {
	merged := mergeByCluster(ps)

	v := View{App: app, Namespace: namespace, Clusters: len(merged)}

	newest := ""
	for _, p := range merged {
		if p.State != StatePresent {
			continue
		}
		v.Present++
		if p.Tag == "" {
			continue
		}
		if newest == "" || promotion.CompareTags(p.Tag, newest) > 0 {
			newest = p.Tag
		}
	}
	v.Newest = newest

	// A tag carrying two digests is worse than a cluster openly behind.
	digests := map[string]bool{}
	for _, p := range merged {
		if p.State == StatePresent && p.Tag == newest && p.Digest != "" {
			digests[p.Digest] = true
		}
	}
	v.MutableTag = len(digests) > 1

	v.Rows = make([]Row, 0, len(merged))
	for _, p := range merged {
		r := Row{Placement: p}
		switch p.State {
		case StatePresent:
			r.DigestPending = p.Digest == ""
			if p.Tag == newest {
				if v.MutableTag && p.Digest != "" {
					r.MutableTag = true
					r.Note = fmt.Sprintf("runs %s, and so does another cluster with a different digest", newest)
				}
				break
			}
			// Behind only when the tags can be ordered. Two build ids differ
			// without one being older, and calling either behind would invent
			// a direction.
			if p.Tag != "" && newest != "" && promotion.CompareTags(p.Tag, newest) < 0 {
				r.Behind = true
				v.Behind++
				r.Note = fmt.Sprintf("behind %s", newest)
			} else if p.Tag != newest {
				r.Note = fmt.Sprintf("runs %s, which cannot be ordered against %s", p.Tag, newest)
			}
		case StateUnreachable:
			r.Note = orElse(p.Reason, "the cluster did not answer")
		case StateDenied:
			r.Note = orElse(p.Reason, "not readable here")
		case StateAbsent:
			r.Note = "not deployed here"
		case StatePending:
			r.Note = "asking"
		}
		v.Rows = append(v.Rows, r)
	}

	sort.SliceStable(v.Rows, func(i, j int) bool {
		si, sj := severity(v.Rows[i]), severity(v.Rows[j])
		if si != sj {
			return si > sj
		}
		return v.Rows[i].Context < v.Rows[j].Context
	})
	return v
}

// severity sorts disagreement to the top, matching the promotion matrix's
// default of showing what needs attention first.
func severity(r Row) int {
	switch {
	case r.MutableTag:
		return 5
	case r.Behind:
		return 4
	case r.State == StateUnreachable:
		return 3
	case r.State == StateDenied:
		return 2
	case r.State == StatePending:
		return 1
	default:
		return 0
	}
}

// mergeByCluster collapses contexts pointing at one cluster.
//
// A kubeconfig commonly holds the same cluster twice under two names, and one
// of the two may carry credentials that no longer work. Counting it twice
// inflates every number on the screen, so the most informative answer wins and
// the other context is recorded as an alias.
func mergeByCluster(ps []Placement) []Placement {
	at := map[string]int{}
	out := make([]Placement, 0, len(ps))
	for _, p := range ps {
		key := p.ClusterID
		if key == "" {
			key = "ctx:" + p.Context
		}
		i, seen := at[key]
		if !seen {
			at[key] = len(out)
			out = append(out, p)
			continue
		}
		if informativeness(p.State) > informativeness(out[i].State) {
			p.Aliases = append(p.Aliases, out[i].Context)
			p.Aliases = append(p.Aliases, out[i].Aliases...)
			out[i] = p
			continue
		}
		out[i].Aliases = append(out[i].Aliases, p.Context)
	}
	return out
}

// informativeness ranks what a cluster told us. Present beats absent, because
// a version is more than a confirmed nothing. Absent beats denied and
// unreachable, because the cluster answered at all.
func informativeness(state string) int {
	switch state {
	case StatePresent:
		return 4
	case StateAbsent:
		return 3
	case StateDenied:
		return 2
	case StateUnreachable:
		return 1
	default:
		return 0
	}
}

func orElse(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

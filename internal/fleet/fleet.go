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

	State string `json:"state"`
	// Reason explains what the state cannot: why a cluster went unreachable or
	// denied, and on a present row, anything the caller had to choose between
	// to produce it.
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
	// MutableTag marks a cluster whose tag another cluster also runs under a
	// different digest. It is not restricted to the newest tag: an older tag
	// resolving to two digests is the same defect.
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
	// DigestUnverified counts present rows whose digest never arrived. A
	// mutable tag cannot be ruled out from a cluster whose image nobody
	// resolved, so a headline reporting agreement can say how much of that
	// agreement rests on digests still in flight.
	DigestUnverified int `json:"digestUnverified,omitempty"`
}

// Build derives the screen from one placement per context.
func Build(app, namespace string, ps []Placement) View {
	merged := mergeByCluster(ps)

	v := View{App: app, Namespace: namespace, Clusters: len(merged)}

	// The newest tag is picked from orderable tags only. A build id loses to
	// nothing, because promotion.CompareTags refuses to order it, so letting
	// it into the maximum would leave it standing as a yardstick every real
	// version then "cannot be ordered against" — one odd tag hiding the whole
	// fleet's drift, and doing it differently depending on the order the
	// caller happened to pass the placements in.
	newest := ""
	for _, p := range merged {
		if p.State != StatePresent {
			continue
		}
		v.Present++
		if !promotion.Orderable(p.Tag) {
			continue
		}
		if newest == "" || promotion.CompareTags(p.Tag, newest) > 0 {
			newest = p.Tag
		}
	}
	v.Newest = newest

	// A tag carrying two digests is worse than a cluster openly behind, and it
	// is that whichever tag it happens to. An older tag resolving to two
	// digests is the same defect as the newest one doing it, so every tag is
	// grouped, not only the leader. An absent tag is grouped with nothing: a
	// tag that does not exist cannot be mutable.
	byTag := map[string]map[string]bool{}
	for _, p := range merged {
		if p.State != StatePresent || p.Tag == "" || p.Digest == "" {
			continue
		}
		if byTag[p.Tag] == nil {
			byTag[p.Tag] = map[string]bool{}
		}
		byTag[p.Tag][p.Digest] = true
	}
	mutable := map[string]bool{}
	for tag, digests := range byTag {
		if len(digests) > 1 {
			mutable[tag] = true
			v.MutableTag = true
		}
	}

	v.Rows = make([]Row, 0, len(merged))
	for _, p := range merged {
		r := Row{Placement: p}
		switch p.State {
		case StatePresent:
			// A caller that already knows the digest is in flight is believed;
			// a row with no digest is pending whether or not it said so.
			r.DigestPending = p.DigestPending || p.Digest == ""
			if r.DigestPending {
				v.DigestUnverified++
			}
			r.MutableTag = p.Digest != "" && mutable[p.Tag]

			verdict := ""
			switch {
			case p.Tag == "":
				// An image pinned by digest carries no tag to compare, and
				// pretending it matches the newest would render the deepest
				// unknown on the screen as the calmest row on it.
				verdict = "version unknown: the image is pinned by digest, so there is no tag to compare"
			case p.Tag == newest:
				// On the newest version anyone could name.
			case promotion.CompareTags(p.Tag, newest) < 0:
				r.Behind = true
				v.Behind++
				verdict = fmt.Sprintf("behind %s", newest)
			case !promotion.Orderable(p.Tag):
				// Two build ids differ without one being older, and calling
				// either behind would invent a direction.
				if newest == "" {
					verdict = fmt.Sprintf("runs %s, which cannot be ordered against any other version here", p.Tag)
				} else {
					verdict = fmt.Sprintf("runs %s, which cannot be ordered against %s", p.Tag, newest)
				}
			default:
				// Orderable, not behind, yet not the newest string: the same
				// version spelled differently, such as v1.0 beside v1.0.0.
				verdict = fmt.Sprintf("runs %s, the same version as %s", p.Tag, newest)
			}

			switch {
			case r.MutableTag && verdict != "":
				r.Note = fmt.Sprintf("%s, and another cluster runs %s with a different digest", verdict, p.Tag)
			case r.MutableTag:
				r.Note = fmt.Sprintf("runs %s, and so does another cluster with a different digest", p.Tag)
			default:
				r.Note = verdict
			}

			// A present row can still carry a reason: the caller had two
			// candidates for this one row and says which it took. It is appended
			// rather than substituted, because the version verdict is what the
			// screen is for and a choice nobody sees is a choice nobody can
			// challenge.
			if p.Reason != "" {
				if r.Note == "" {
					r.Note = p.Reason
				} else {
					r.Note += "; " + p.Reason
				}
			}
		case StateUnreachable:
			r.Note = orElse(p.Reason, "the cluster did not answer")
		case StateDenied:
			r.Note = orElse(p.Reason, "not readable here")
		case StateAbsent:
			r.Note = "not deployed here"
		case StatePending:
			r.Note = "asking"
		default:
			// A state nobody set is a question nobody answered. Rendering it
			// as a blank row would put an unknown cluster among the healthy
			// ones, so it reads as pending and says why.
			r.State = StatePending
			r.Note = "state not reported; treated as not yet answered"
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
//
// A cluster nobody reached outranks a cluster whose older version you can see.
// The promotion matrix decides the same way for the same reason: "neither
// agreement nor a defect can be claimed from clusters we never saw", so the
// version we could not read might be worse than the old one we could. The two
// screens rank the same facts by one rule.
//
// The same rule orders the rest: the less a cluster told us, the higher it
// sits. A denied read says nothing about the app, so it outranks a present
// cluster whose version could not be named, which at least confirmed the app
// runs there.
//
// Absent shares the bottom bucket with a cluster on the newest version. That is
// deliberate: an app not deployed somewhere is a schedule, not a disagreement,
// so "not deployed here" rows interleave alphabetically with the healthy ones
// rather than climbing the screen. The states stay distinct in the data.
func severity(r Row) int {
	switch {
	case r.MutableTag:
		return 6
	case r.State == StateUnreachable:
		return 5
	case r.Behind:
		return 4
	case r.State == StateDenied:
		return 3
	case r.State == StatePresent && r.Tag == "":
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
//
// A placement with no ClusterID keys on its context name instead, so each
// unidentified context keeps its own row. Sharing one empty key would merge
// every unidentified cluster into a single row and delete clusters outright,
// which is the worse error for a package whose job is to lose none. This puts
// an invariant on whoever builds the placements: ClusterID must come from the
// kubeconfig cluster entry, not from a successful connection. Populate it from
// a connection and an unreachable duplicate context arrives with an empty
// ClusterID, fails to merge with its reachable twin, and inflates Clusters in
// exactly the case this merge exists to prevent.
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
		// Aliases are rebuilt into a fresh slice on both paths. Appending to
		// the caller's slice would write through the backing array it still
		// holds.
		if informativeness(p.State) > informativeness(out[i].State) {
			p.Aliases = appendAliases(p.Aliases, append([]string{out[i].Context}, out[i].Aliases...)...)
			out[i] = p
			continue
		}
		out[i].Aliases = appendAliases(out[i].Aliases, p.Context)
	}
	return out
}

// appendAliases returns a new slice, never extending the one passed in.
func appendAliases(existing []string, add ...string) []string {
	out := make([]string, 0, len(existing)+len(add))
	out = append(out, existing...)
	return append(out, add...)
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

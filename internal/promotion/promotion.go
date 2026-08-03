// Package promotion answers "is the fix in prod yet".
//
// One row per app, one column per environment, sorted so the apps whose
// environments disagree float to the top. The matrix compares and never
// promotes: no control here mutates anything, because deploying belongs to the
// pipeline that owns it.
//
// Two distinctions carry most of the value. Ahead is worse than behind: behind
// is a schedule, ahead is an out-of-band change. And absent is not forbidden:
// "it is not deployed there" and "I cannot see it" are different facts, and a
// matrix that renders them the same way is a matrix that lies during an
// incident.
package promotion

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Cell states.
const (
	StateSame          = "same"
	StateBehind        = "behind"
	StateAhead         = "ahead"
	StateDiffers       = "differs"
	StateDigestDiffers = "digest-differs"
	StateAbsent        = "absent"
	StateDenied        = "denied"
	// StateSplit is an environment whose clusters do not agree. It never
	// carries a version, because picking one would be the guess this state
	// exists to prevent.
	StateSplit = "split"
)

// Env is one column.
type Env struct {
	Name string `json:"name"`
	Risk string `json:"risk"`
	// Context is the first context bound to this environment, kept so a cell
	// can still be opened. Contexts is all of them.
	Context  string   `json:"context,omitempty"`
	Contexts []string `json:"contexts,omitempty"`
	// Unreadable names the contexts that did not answer. A cell in an
	// environment with any unreadable context can never claim agreement,
	// because the clusters nobody read might disagree.
	Unreadable []string `json:"unreadable,omitempty"`
}

// Instance is one app as it exists in one environment.
type Instance struct {
	Env       string
	Context   string
	App       string
	Namespace string

	Present bool
	// Denied marks an environment that could not be read. It is deliberately
	// not the same as absent.
	Denied       bool
	DeniedReason string

	Image  string
	Tag    string
	Digest string

	Health     string
	Ready      string
	RevisionAt string
}

// Cell is one instance, compared against its upstream.
type Cell struct {
	Env       string `json:"env"`
	Namespace string `json:"namespace,omitempty"`
	State     string `json:"state"`
	Tag       string `json:"tag,omitempty"`
	Image     string `json:"image,omitempty"`
	Digest    string `json:"digest,omitempty"`
	// DigestPending marks a cell whose digest has not arrived. Tag comparison
	// never waits for it, and a missing digest never reads as a match.
	DigestPending bool   `json:"digestPending,omitempty"`
	Health        string `json:"health,omitempty"`
	Ready         string `json:"ready,omitempty"`
	RevisionAt    string `json:"revisionAt,omitempty"`
	Note          string `json:"note,omitempty"`
	// Severe marks the states that deserve the error hue rather than a warning:
	// ahead of upstream, and a tag that resolves to a different digest.
	Severe bool `json:"severe,omitempty"`
	// Clusters is how many contexts back this cell. Zero and one both mean a
	// single cluster; the UI shows the count only above one.
	Clusters int `json:"clusters,omitempty"`
}

// Row is one app across every environment.
type Row struct {
	App       string `json:"app"`
	Namespace string `json:"namespace"`
	Cells     []Cell `json:"cells"`
	Drift     int    `json:"drift"`
	Ahead     bool   `json:"ahead,omitempty"`
}

// Summary is what the page header reports.
type Summary struct {
	Apps    int `json:"apps"`
	Drifted int `json:"drifted"`
	Ahead   int `json:"ahead"`
	Split   int `json:"split"`
}

// Summarize counts the rows worth looking at.
func Summarize(rows []Row) Summary {
	s := Summary{Apps: len(rows)}
	for _, r := range rows {
		if r.Drift > 0 {
			s.Drifted++
		}
		if r.Ahead {
			s.Ahead++
		}
		for _, c := range r.Cells {
			if c.State == StateSplit {
				s.Split++
				break
			}
		}
	}
	return s
}

// Build lays out the matrix.
func Build(envs []Env, instances []Instance) []Row {
	byApp := map[string][]Instance{}
	order := []string{}
	for _, in := range instances {
		key := identity(in)
		if _, seen := byApp[key]; !seen {
			order = append(order, key)
		}
		byApp[key] = append(byApp[key], in)
	}

	rows := make([]Row, 0, len(order))
	for _, key := range order {
		rows = append(rows, row(envs, byApp[key]))
	}

	// Drift first, then name: the matrix is read for disagreement, not for the
	// alphabet.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Ahead != rows[j].Ahead {
			return rows[i].Ahead
		}
		if rows[i].Drift != rows[j].Drift {
			return rows[i].Drift > rows[j].Drift
		}
		return rows[i].App < rows[j].App
	})
	return rows
}

func row(envs []Env, instances []Instance) Row {
	byEnv := map[string][]Instance{}
	for _, in := range instances {
		byEnv[in.Env] = append(byEnv[in.Env], in)
	}

	out := Row{App: instances[0].App, Namespace: instances[0].Namespace}

	// upstream is the last environment that actually told us something. An
	// unreadable environment is not an agreement, and neither is a split one,
	// so neither becomes the thing the next column is compared against.
	var upstream *Instance

	for _, env := range envs {
		rep, cell, ok := resolve(env, byEnv[env.Name], upstream)
		out.Cells = append(out.Cells, cell)
		// Absent and denied are not comparisons: there is nothing to compare
		// against, or nothing we were allowed to read. Only a cell that was
		// actually evaluated moves the drift count. A split cell was
		// evaluated, and disagreed, so it counts.
		if cell.State != StateSame && cell.State != StateAbsent && cell.State != StateDenied {
			out.Drift++
		}
		if cell.State == StateAhead {
			out.Ahead = true
		}
		if ok {
			copyOf := rep
			upstream = &copyOf
		}
	}
	return out
}

// resolve turns one environment's clusters into one cell.
//
// The third return says whether the cell may serve as upstream for the next
// column. Absent, denied, and split all say no: a column the matrix could not
// read, or whose clusters disagree, is not a version the next column can be
// compared against.
func resolve(env Env, group []Instance, upstream *Instance) (Instance, Cell, bool) {
	clusters := len(env.Contexts)
	if clusters == 0 {
		clusters = 1
	}
	// Unreadable can outgrow the declared Contexts in a synthetically built
	// Env (Env.Contexts unset but Unreadable populated); service.go never
	// produces that shape today, since acc.contexts is always a superset of
	// acc.unreadable, but resolve() must not print a cluster count smaller
	// than the number of contexts it names as unread.
	if unread := len(env.Unreadable); unread > clusters {
		clusters = unread
	}

	var present []Instance
	denied := 0
	for _, in := range group {
		switch {
		case in.Denied:
			denied++
		case in.Present:
			present = append(present, in)
		}
	}

	// Namespace, when the instance is known: an absent or denied cell still
	// names where it would have lived, because the app identity that got us
	// here came from a real instance somewhere in this group. Take the first
	// non-empty one, since a denied instance can carry an unknown (empty)
	// namespace even when a later instance in the same group knows it.
	ns := ""
	for _, in := range group {
		if in.Namespace != "" {
			ns = in.Namespace
			break
		}
	}

	if len(present) == 0 {
		if denied > 0 {
			reason := "not readable"
			for _, in := range group {
				if in.Denied && in.DeniedReason != "" {
					reason = in.DeniedReason
					break
				}
			}
			return Instance{}, Cell{Env: env.Name, Namespace: ns, State: StateDenied, Note: reason, Clusters: clusters}, false
		}

		// service.go's denied placeholders carry no App name and are filtered
		// out before Build ever sees them, so a fully or partially unread
		// environment reaches here with denied == 0 and an empty group. Absent
		// requires every context to have actually answered; anything less is a
		// fact this cell must not erase.
		if len(env.Unreadable) > 0 {
			unread := len(env.Unreadable)
			if unread >= clusters {
				note := fmt.Sprintf("%d of %d clusters did not answer", unread, clusters)
				return Instance{}, Cell{Env: env.Name, Namespace: ns, State: StateDenied, Note: note, Clusters: clusters}, false
			}
			// Some, but not all, contexts answered, and none of them have the
			// app. That is not "not deployed here": the clusters that stayed
			// silent might. Name what was read and what was not, same as the
			// fully-unread case, rather than inventing a disagreement no
			// instance actually showed. Terse, like every other split() note:
			// this renders inside a matrix cell.
			readable := clusters - unread
			note := fmt.Sprintf("not in the %d clusters read; %d did not answer", readable, unread)
			return Instance{}, Cell{Env: env.Name, Namespace: ns, State: StateDenied, Note: note, Clusters: clusters}, false
		}

		return Instance{}, Cell{Env: env.Name, Namespace: ns, State: StateAbsent, Note: "not deployed here", Clusters: clusters}, false
	}

	// More present instances than the environment declares clusters means the
	// declared count cannot be trusted here: this is typically two namespaces
	// (e.g. "shop" and "shop-prod") collapsing to one app identity inside a
	// single environment, not genuinely distinct clusters. A coverage
	// fraction built from that count would be a number known to be wrong, so
	// decide on consensus alone and never cite the cluster count.
	//
	// But an unreadable context still wins over all of that: the clusters
	// nobody read might disagree, so this can never collapse to agreement
	// just because the ones that did answer agree with each other.
	if len(present) > clusters {
		if len(env.Unreadable) == 0 {
			if rep, agreed := consensus(present); agreed {
				return rep, compare(rep, upstream), true
			}
		}
		return Instance{}, splitOnConsensusOnly(env, present), false
	}

	readable := clusters - len(env.Unreadable)
	if rep, agreed := consensus(present); agreed && len(present) == readable && len(env.Unreadable) == 0 {
		c := compare(rep, upstream)
		c.Clusters = clusters
		return rep, c, true
	}

	return Instance{}, split(env, present, clusters, readable), false
}

// consensus collapses clusters that agree.
//
// Tags must match exactly. Digests must match only when both arrived, because
// a digest still in flight is not a disagreement. The representative carries
// no digest unless every cluster reported the same one, so a pending fetch
// renders as pending instead of borrowing one cluster's answer for the rest.
func consensus(present []Instance) (Instance, bool) {
	rep := present[0]
	anyPending := rep.Digest == ""
	for _, in := range present[1:] {
		if in.Tag != rep.Tag {
			return Instance{}, false
		}
		if in.Digest == "" {
			anyPending = true
			continue
		}
		if rep.Digest != "" && in.Digest != rep.Digest {
			return Instance{}, false
		}
		if rep.Digest == "" {
			rep.Digest = in.Digest
		}
	}
	if anyPending {
		rep.Digest = ""
	}
	rep.Context = ""
	return rep, true
}

// split is the cell for an environment whose clusters do not agree. It names
// the disagreement and never names a version.
func split(env Env, present []Instance, clusters, readable int) Cell {
	c := Cell{Env: env.Name, State: StateSplit, Severe: true, Clusters: clusters}
	if len(present) > 0 {
		c.Namespace = present[0].Namespace
	}

	tags := map[string]bool{}
	digests := map[string]bool{}
	for _, in := range present {
		tags[in.Tag] = true
		if in.Digest != "" {
			digests[in.Digest] = true
		}
	}

	switch {
	case len(env.Unreadable) > 0:
		// A cluster nobody read might disagree, so unreadable always wins:
		// neither agreement nor a defect can be claimed from clusters we
		// never saw.
		c.Note = fmt.Sprintf("%d of %d clusters readable; the rest did not answer", readable, clusters)
	case len(tags) == 1 && len(digests) > 1:
		// A mutable tag outranks partial coverage. One tag resolving to two
		// digests is a defect; a cluster not having the app yet is a
		// schedule, and the defect is the worse fact to hide. Cite the
		// clusters that actually reported the tag, not the declared total —
		// this branch is reachable under partial coverage too, where fewer
		// clusters answered than the environment declares.
		c.Tag = ""
		c.Note = fmt.Sprintf("one tag across %d clusters but %d digests: the tag is mutable and these are not the same code", len(present), len(digests))
	case len(present) < readable:
		c.Note = fmt.Sprintf("deployed in %d of %d clusters", len(present), clusters)
	default:
		c.Note = fmt.Sprintf("%d versions across %d clusters", len(tags), clusters)
	}
	return c
}

// splitOnConsensusOnly is the cell for an environment where more instances
// are present than the environment declares clusters for (see resolve). The
// declared cluster count is not meaningful in that case, so this never prints
// a coverage fraction or a cluster count.
func splitOnConsensusOnly(env Env, present []Instance) Cell {
	c := Cell{Env: env.Name, State: StateSplit, Severe: true}
	if len(present) > 0 {
		c.Namespace = present[0].Namespace
	}

	tags := map[string]bool{}
	for _, in := range present {
		tags[in.Tag] = true
	}
	c.Note = fmt.Sprintf("%d versions present in %s", len(tags), env.Name)
	return c
}

func compare(in Instance, upstream *Instance) Cell {
	c := Cell{
		Env: in.Env, Namespace: in.Namespace, State: StateSame,
		Tag: in.Tag, Image: in.Image, Digest: in.Digest,
		Health: in.Health, Ready: in.Ready, RevisionAt: in.RevisionAt,
	}
	if upstream == nil {
		// The first environment has nothing upstream of it. That is not
		// agreement, it is simply where the comparison starts.
		c.DigestPending = in.Digest == ""
		return c
	}

	if in.Tag != upstream.Tag {
		switch order := CompareTags(in.Tag, upstream.Tag); {
		case order < 0:
			c.State = StateBehind
			c.Note = fmt.Sprintf("behind %s, which runs %s", upstream.Env, upstream.Tag)
		case order > 0:
			// Behind is a schedule. Ahead is a change nobody upstream made.
			c.State = StateAhead
			c.Severe = true
			c.Note = fmt.Sprintf("ahead of %s, which runs %s; nothing promoted this", upstream.Env, upstream.Tag)
		default:
			c.State = StateDiffers
			c.Note = fmt.Sprintf("differs from %s (%s), and the tags cannot be ordered", upstream.Env, upstream.Tag)
		}
		c.DigestPending = in.Digest == ""
		return c
	}

	// Same tag. Now the digest decides whether that means the same code.
	switch {
	case in.Digest == "" || upstream.Digest == "":
		c.DigestPending = true
	case in.Digest != upstream.Digest:
		c.State = StateDigestDiffers
		c.Severe = true
		c.Note = fmt.Sprintf("same tag as %s but a different digest: the tag is mutable and these are not the same code", upstream.Env)
	}
	return c
}

// Identity is how one app is recognized across environments.
//
// Name plus namespace, with an environment suffix or prefix tolerated, because
// team-a-qa and team-a-prod are one team's namespace in two places and lining
// those up is the whole point of the view.
func Identity(app, namespace string) string {
	return app + "|" + StripEnvToken(namespace)
}

func identity(in Instance) string { return Identity(in.App, in.Namespace) }

var envTokens = map[string]bool{
	"qa": true, "stg": true, "stage": true, "staging": true, "prod": true,
	"production": true, "prd": true, "dev": true, "test": true, "uat": true,
	"preprod": true, "sandbox": true,
}

// StripEnvToken removes the environment token from a namespace, so that
// team-a-qa and team-a-prod resolve to one identity.
func StripEnvToken(ns string) string {
	parts := strings.Split(ns, "-")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if envTokens[strings.ToLower(strings.TrimRight(p, "0123456789"))] {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return ns
	}
	return strings.Join(kept, "-")
}

// TagOf reads the tag from an image reference.
//
// A registry port is not a tag: reading "registry:5000/app" as tag 5000 would
// put a port number in every cell of a self-hosted registry's matrix.
func TagOf(image string) string {
	// No image is not an image running latest. A metadata-only read leaves this
	// empty, and the implicit-latest rule below would turn "unknown" into a
	// version claim.
	if image == "" {
		return ""
	}
	if at := strings.Index(image, "@"); at >= 0 {
		// Digest-pinned. There is no tag to show, and inventing one would be
		// worse than an empty cell.
		return ""
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[colon+1:]
	}
	return "latest"
}

// versionLike is what can be ordered: an optional v, then dotted numbers, then
// an optional suffix. A tag like "sha-abc123" contains digits but is a build
// id, and ordering two of those would invent a direction.
var versionLike = regexp.MustCompile(`^v?\d+(\.\d+)*([-+._].*)?$`)

var numeric = regexp.MustCompile(`\d+`)

// CompareTags orders two version tags, returning zero when they cannot be
// ordered. Claiming a direction we cannot establish would be worse than saying
// they merely differ.
func CompareTags(a, b string) int {
	an, aok := versionParts(a)
	bn, bok := versionParts(b)
	if !aok || !bok {
		return 0
	}

	for i := 0; i < len(an) || i < len(bn); i++ {
		x, y := 0, 0
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}

	// Numerically equal. A release candidate is behind the release it is a
	// candidate for, which is the one suffix rule worth encoding.
	aPre, bPre := hasPrerelease(a), hasPrerelease(b)
	switch {
	case aPre && !bPre:
		return -1
	case !aPre && bPre:
		return 1
	}
	return 0
}

func versionParts(tag string) ([]int, bool) {
	if !versionLike.MatchString(tag) {
		return nil, false
	}
	found := numeric.FindAllString(tag, -1)
	if len(found) == 0 {
		return nil, false
	}
	out := make([]int, 0, len(found))
	for _, f := range found {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func hasPrerelease(tag string) bool {
	t := strings.ToLower(tag)
	for _, marker := range []string{"-rc", "-alpha", "-beta", "-pre", "-snapshot"} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}

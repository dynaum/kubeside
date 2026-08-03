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
)

// Env is one column.
type Env struct {
	Name    string `json:"name"`
	Risk    string `json:"risk"`
	Context string `json:"context,omitempty"`
}

// Instance is one app as it exists in one environment.
type Instance struct {
	Env       string
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
	byEnv := map[string]Instance{}
	for _, in := range instances {
		byEnv[in.Env] = in
	}

	out := Row{App: instances[0].App, Namespace: instances[0].Namespace}

	// upstream is the last environment that actually told us something. An
	// unreadable environment is not an agreement, so it does not become the
	// thing the next column is compared against.
	var upstream *Instance

	for _, env := range envs {
		in, ok := byEnv[env.Name]
		if !ok {
			out.Cells = append(out.Cells, Cell{Env: env.Name, State: StateAbsent, Note: "not deployed here"})
			continue
		}
		if in.Denied {
			out.Cells = append(out.Cells, Cell{
				Env: env.Name, Namespace: in.Namespace, State: StateDenied,
				Note: orElse(in.DeniedReason, "not readable"),
			})
			continue
		}
		if !in.Present {
			out.Cells = append(out.Cells, Cell{
				Env: env.Name, Namespace: in.Namespace, State: StateAbsent, Note: "not deployed here",
			})
			continue
		}

		c := compare(in, upstream)
		out.Cells = append(out.Cells, c)
		if c.State != StateSame {
			out.Drift++
		}
		if c.State == StateAhead {
			out.Ahead = true
		}
		copyOf := in
		upstream = &copyOf
	}
	return out
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

func orElse(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

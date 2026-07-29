package resolved

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Comparing one app's configuration across two environments.
//
// The useful answer is not "these 34 keys differ". Half of them are supposed
// to: a database URL that named the same host in staging and production would
// be the actual bug. So every row is classified, and every classification names
// the rule that produced it, because a verdict nobody can audit is a verdict
// nobody should trust.
//
// Two of the classes exist for differences that are not differences at all. A
// value identical across environments can be the finding: a development log
// level that survived into production, capacity carried over unchanged, or one
// credential shared by staging and prod. Those are the failures nobody notices
// until something fills up.

// Classifications.
const (
	// ClassUnknown means no comparison was possible, usually a secret nobody
	// may read on one side.
	ClassUnknown = ""
	// ClassMatch is identical and unremarkable.
	ClassMatch = "match"
	// ClassExpected differs for a reason the values themselves explain.
	ClassExpected = "expected"
	// ClassDrift differs with no explanation.
	ClassDrift = "drift"
	// ClassSuspicious matches where matching is the problem.
	ClassSuspicious = "suspicious"
	// ClassMissing is present on one side only.
	ClassMissing = "missing"
)

// Env is the side of a comparison: its name and how dangerous it is.
type Env struct {
	Name string `json:"name"`
	Risk string `json:"risk"` // low, medium, high
}

// CrossRow is one key compared across two environments.
type CrossRow struct {
	Key   string `json:"key"`
	Left  string `json:"left,omitempty"`
	Right string `json:"right,omitempty"`
	// LeftUnset and RightUnset distinguish a key that is not set from one set
	// to the empty string, which are different configurations.
	LeftUnset  bool `json:"leftUnset,omitempty"`
	RightUnset bool `json:"rightUnset,omitempty"`
	// Masked marks a secret-sourced row. Its values never appear; only digests
	// are compared, so production credentials never sit beside staging ones on
	// a screen.
	Masked      bool   `json:"masked,omitempty"`
	LeftDigest  string `json:"leftDigest,omitempty"`
	RightDigest string `json:"rightDigest,omitempty"`
	Class       string `json:"class"`
	Reason      string `json:"reason,omitempty"`
	LeftSource  Source `json:"leftSource,omitzero"`
	RightSource Source `json:"rightSource,omitzero"`
}

// Summary counts each class, which is what the topbar reports.
type Summary struct {
	Drift      int `json:"drift"`
	Suspicious int `json:"suspicious"`
	Missing    int `json:"missing"`
	Expected   int `json:"expected"`
	Match      int `json:"match"`
	Unknown    int `json:"unknown"`
}

// Summarize counts rows by class.
func Summarize(rows []CrossRow) Summary {
	var s Summary
	for _, r := range rows {
		switch r.Class {
		case ClassDrift:
			s.Drift++
		case ClassSuspicious:
			s.Suspicious++
		case ClassMissing:
			s.Missing++
		case ClassExpected:
			s.Expected++
		case ClassMatch:
			s.Match++
		default:
			s.Unknown++
		}
	}
	return s
}

// CrossDiff compares two containers' resolved configuration.
func CrossDiff(left, right Container, leftEnv, rightEnv Env) []CrossRow {
	byKey := map[string]*CrossRow{}
	get := func(key string) *CrossRow {
		if r, ok := byKey[key]; ok {
			return r
		}
		r := &CrossRow{Key: key, LeftUnset: true, RightUnset: true}
		byKey[key] = r
		return r
	}

	for _, v := range left.Values {
		r := get(v.Key)
		r.LeftUnset = false
		r.LeftSource = v.Source
		if v.Masked {
			r.Masked, r.LeftDigest = true, v.Digest
			continue
		}
		r.Left = v.Value
	}
	for _, v := range right.Values {
		r := get(v.Key)
		r.RightUnset = false
		r.RightSource = v.Source
		if v.Masked {
			r.Masked, r.RightDigest = true, v.Digest
			continue
		}
		r.Right = v.Value
	}

	out := make([]CrossRow, 0, len(byKey))
	for _, r := range byKey {
		r.Class, r.Reason = classify(*r, leftEnv, rightEnv)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func classify(r CrossRow, left, right Env) (string, string) {
	if r.LeftUnset != r.RightUnset {
		absent, present := right.Name, left.Name
		if r.RightUnset == false {
			absent, present = left.Name, right.Name
		}
		return ClassMissing, fmt.Sprintf("set in %s, not set in %s", present, absent)
	}

	if r.Masked {
		return classifySecret(r, left, right)
	}

	// The downward API resolves at container start, so these differ by
	// definition. A pod IP reported as drift would be noise on every comparison
	// anybody ever runs.
	if isRuntime(r.LeftSource) || isRuntime(r.RightSource) {
		return ClassExpected, "resolved when each container started, so it differs by definition"
	}

	if r.Left != r.Right {
		if mentions(r.Left, left.Name) || mentions(r.Right, right.Name) {
			return ClassExpected, "each value names its own environment, so differing is the arrangement working"
		}
		if looksEnvironmental(r.Key, r.Left, r.Right) {
			return ClassExpected, "an address or endpoint, which is expected to differ per environment"
		}
		return ClassDrift, "differs with nothing in the values to explain why"
	}

	// Identical. Most of the time that is the answer; sometimes it is the
	// finding.
	if mentions(r.Left, left.Name) && left.Name != right.Name {
		return ClassSuspicious, fmt.Sprintf("both environments use a value naming %s, so %s is pointing at %s", left.Name, right.Name, left.Name)
	}
	if mentions(r.Left, right.Name) && left.Name != right.Name {
		return ClassSuspicious, fmt.Sprintf("both environments use a value naming %s", right.Name)
	}
	if riskier(left, right) || riskier(right, left) {
		if isDevelopmentSetting(r.Key, r.Left) {
			return ClassSuspicious, "a development setting, identical in an environment where it is riskier"
		}
		if isCapacity(r.Key) && isNumeric(r.Left) {
			return ClassSuspicious, "capacity carried across environments of different risk without being raised"
		}
	}
	return ClassMatch, ""
}

// classifySecret compares by digest and never by value. The tool can report
// that two credentials differ without ever placing them on one screen.
func classifySecret(r CrossRow, left, right Env) (string, string) {
	if r.LeftDigest == "" || r.RightDigest == "" {
		return ClassUnknown, "not compared: reading one of these Secrets was not permitted, and a secret is never guessed at"
	}
	if r.LeftDigest != r.RightDigest {
		return ClassExpected, "different credentials per environment, compared by digest"
	}
	if riskier(left, right) || riskier(right, left) {
		return ClassSuspicious, fmt.Sprintf("%s and %s hold the same credential", left.Name, right.Name)
	}
	return ClassMatch, "identical by digest"
}

var riskOrder = map[string]int{"low": 1, "medium": 2, "high": 3}

// riskier reports whether a is meaningfully more dangerous than b.
func riskier(a, b Env) bool {
	return riskOrder[a.Risk] > riskOrder[b.Risk]
}

// mentions looks for an environment's name as a whole token inside a value, so
// "stg-db.internal" matches stg and "strategy" does not.
func mentions(value, env string) bool {
	if value == "" || env == "" {
		return false
	}
	re := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(strings.ToLower(env)) + `([^a-z0-9]|$)`)
	return re.MatchString(strings.ToLower(value))
}

var addressish = regexp.MustCompile(`^[a-z]+://|\.(internal|local|svc|com|net|io)(\b|:)|:\d{2,5}$`)

// looksEnvironmental reports whether a value is the kind of thing that points
// somewhere, which is the category that is supposed to differ per environment.
func looksEnvironmental(key, a, b string) bool {
	k := strings.ToUpper(key)
	for _, suffix := range []string{"_URL", "_URI", "_HOST", "_ENDPOINT", "_ADDR", "_ADDRESS", "_DSN", "_BROKER", "_BUCKET"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return addressish.MatchString(strings.ToLower(a)) && addressish.MatchString(strings.ToLower(b))
}

var developmentValues = map[string]bool{
	"debug": true, "trace": true, "verbose": true, "development": true, "dev": true,
}

// isDevelopmentSetting recognizes the settings that are correct in a sandbox
// and expensive in production.
func isDevelopmentSetting(key, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if developmentValues[v] {
		return true
	}
	k := strings.ToUpper(key)
	if (strings.Contains(k, "DEBUG") || strings.Contains(k, "PROFIL") || strings.Contains(k, "INSECURE")) &&
		(v == "true" || v == "1" || v == "on" || v == "yes") {
		return true
	}
	return false
}

func isRuntime(src Source) bool {
	return src.Kind == SourceDownward || src.Kind == SourceResource
}

func isCapacity(key string) bool {
	k := strings.ToUpper(key)
	for _, hint := range []string{"CONNECTIONS", "POOL", "LIMIT", "MAX_", "_SIZE", "REPLICAS", "WORKERS", "THREADS", "QUOTA"} {
		if strings.Contains(k, hint) {
			return true
		}
	}
	return false
}

func isNumeric(v string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil && v != ""
}

// digestKey is generated once per process and never leaves it.
//
// An unkeyed hash of a secret is a verifier for anybody who obtains one. They
// guess a value, hash it, and compare; truncation does not help, because the
// threat is confirming a guess rather than inverting a hash, and a wordlist
// runs offline. Digests reach the browser, and from there a screenshot, a
// pasted response, or a shared screen.
//
// Keying it kills that. Both sides of a comparison are hashed in this process,
// so equality still works, and nothing persists a digest, so a fresh key on
// every run costs nothing and rules out precomputation across runs.
var digestKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		// A process that cannot read randomness cannot make this promise, and
		// carrying on with a predictable key would keep the appearance of one.
		panic("kubeside: no entropy for the digest key: " + err.Error())
	}
	return k
}()

// Digest fingerprints a secret value for comparison.
//
// The digest exists so two environments' credentials can be compared without
// either being read onto a screen. It is truncated because nobody needs 64
// characters to see that two fingerprints differ.
func Digest(value []byte) string {
	mac := hmac.New(sha256.New, digestKey)
	mac.Write(value)
	return "sha256:" + hex.EncodeToString(mac.Sum(nil))[:12]
}

package api

import "slices"

// The delta protocol. The browser subscribes to a view; the server answers with
// one snapshot and then patches for as long as the subscription lives. The
// browser never polls, and a rollout that flips one row sends one row.
//
// Message types are strings rather than numbers so a captured frame is readable
// without a decoder ring.

// Views the stream can serve. Anything else is refused by name rather than
// silently ignored, so a client bug surfaces as an error instead of a screen
// that never updates.
const ViewApps = "apps"

// Client to server.
const (
	msgSubscribe   = "subscribe"
	msgUnsubscribe = "unsubscribe"
)

// Server to client.
const (
	msgSnapshot = "snapshot"
	msgPatch    = "patch"
	msgError    = "error"
)

// ClientMessage is what the browser sends. Both fields are required for both
// message types: a subscription is identified by the pair.
type ClientMessage struct {
	Type    string `json:"type"`
	View    string `json:"view"`
	Context string `json:"context"`
}

// ServerMessage is what the browser receives. Exactly one payload field is
// populated, chosen by Type.
type ServerMessage struct {
	Type    string `json:"type"`
	View    string `json:"view"`
	Context string `json:"context"`
	// Seq increments per subscription, starting at the snapshot. A client that
	// sees a gap knows it missed a patch and must resubscribe.
	Seq      int64      `json:"seq"`
	Snapshot *AppsView  `json:"snapshot,omitempty"`
	Patch    *AppsPatch `json:"patch,omitempty"`
	Message  string     `json:"message,omitempty"`
}

// AppsPatch carries only what moved. Absent fields mean unchanged, never empty:
// an omitted Removed is not "everything was deleted".
type AppsPatch struct {
	Meta    *AppsMeta `json:"meta,omitempty"`
	Added   []AppView `json:"added,omitempty"`
	Changed []AppView `json:"changed,omitempty"`
	Removed []string  `json:"removed,omitempty"`
}

// AppsMeta is the view-level truth: what scope was readable, what state the
// connection is in, what could not be read. It travels only when it changes,
// but it must travel, because a cluster going stale changes what the screen is
// allowed to claim.
type AppsMeta struct {
	State   string      `json:"state"`
	Scope   string      `json:"scope"`
	Reason  string      `json:"reason,omitempty"`
	Partial []string    `json:"partial,omitempty"`
	Error   string      `json:"error,omitempty"`
	Metrics MetricsInfo `json:"metrics"`
}

func metaOf(v AppsView) AppsMeta {
	return AppsMeta{
		State:   v.State,
		Scope:   v.Scope,
		Reason:  v.Reason,
		Partial: v.Partial,
		Error:   v.Error,
		Metrics: v.Metrics,
	}
}

func (m AppsMeta) equal(o AppsMeta) bool {
	return m.State == o.State &&
		m.Scope == o.Scope &&
		m.Reason == o.Reason &&
		m.Error == o.Error &&
		m.Metrics == o.Metrics &&
		slices.Equal(m.Partial, o.Partial)
}

// appKey identifies a row. Grouping already guarantees one app per
// namespace/name pair, so that pair is the identity the client keys on.
func appKey(a AppView) string { return a.Namespace + "/" + a.Name }

// diffApps computes the patch that turns prev into next. The second return is
// false when nothing moved, which is the common case between two polls and must
// cost the client nothing.
func diffApps(prev, next AppsView) (AppsPatch, bool) {
	var patch AppsPatch

	if m, p := metaOf(next), metaOf(prev); !m.equal(p) {
		patch.Meta = &m
	}

	before := make(map[string]AppView, len(prev.Apps))
	for _, a := range prev.Apps {
		before[appKey(a)] = a
	}

	for _, a := range next.Apps {
		k := appKey(a)
		old, existed := before[k]
		switch {
		case !existed:
			patch.Added = append(patch.Added, a)
		case old != a:
			patch.Changed = append(patch.Changed, a)
		}
		delete(before, k)
	}

	for k := range before {
		patch.Removed = append(patch.Removed, k)
	}
	// Map iteration is unordered; a stable wire message is easier to read in a
	// capture and easier to assert on.
	slices.Sort(patch.Removed)

	changed := patch.Meta != nil || len(patch.Added) > 0 || len(patch.Changed) > 0 || len(patch.Removed) > 0
	return patch, changed
}

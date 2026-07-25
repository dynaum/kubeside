package api

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dynaum/kubeside/internal/logs"
)

// The delta protocol. The browser subscribes to a view; the server answers with
// one snapshot and then patches for as long as the subscription lives. The
// browser never polls, and a rollout that flips one row sends one row.
//
// Message types are strings rather than numbers so a captured frame is readable
// without a decoder ring.

// Views the stream can serve. Anything else is refused by name rather than
// silently ignored, so a client bug surfaces as an error instead of a screen
// that never updates.
const (
	ViewApps = "apps"
	ViewLogs = "logs"
)

// Client to server.
const (
	msgSubscribe   = "subscribe"
	msgUnsubscribe = "unsubscribe"
)

// Server to client.
const (
	msgSnapshot = "snapshot"
	msgPatch    = "patch"
	msgLogs     = "logs"
	msgError    = "error"
)

// ClientMessage is what the browser sends. Type, View, and Context are always
// required; the rest identify and shape a logs subscription.
//
// Every field that changes what a subscription reads is part of its identity,
// so one window revealing sidecars cannot change what another window sees.
type ClientMessage struct {
	Type    string `json:"type"`
	View    string `json:"view"`
	Context string `json:"context"`

	Namespace string `json:"namespace,omitempty"`
	Workload  string `json:"workload,omitempty"`

	Pods            []string `json:"pods,omitempty"`
	Containers      []string `json:"containers,omitempty"`
	IncludeSidecars bool     `json:"includeSidecars,omitempty"`
	IncludeInit     bool     `json:"includeInit,omitempty"`
	Previous        bool     `json:"previous,omitempty"`
	Tail            int64    `json:"tail,omitempty"`
}

// subscriptionKey is the identity of one subscription. Two clients asking for
// the same thing share a feed; asking for anything different does not.
func (m ClientMessage) subscriptionKey() string {
	if m.View != ViewLogs {
		return m.View + "|" + m.Context
	}
	var b strings.Builder
	b.WriteString(m.View + "|" + m.Context + "|" + m.Namespace + "/" + m.Workload)
	b.WriteString("|pods=" + strings.Join(m.Pods, ","))
	b.WriteString("|containers=" + strings.Join(m.Containers, ","))
	fmt.Fprintf(&b, "|sidecars=%t|init=%t|previous=%t|tail=%d", m.IncludeSidecars, m.IncludeInit, m.Previous, m.Tail)
	return b.String()
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
	Logs     *LogsBatch `json:"logs,omitempty"`
	Message  string     `json:"message,omitempty"`
}

// LogsBatch is a slice of merged output. Lines are batched rather than sent one
// per frame: a workload emitting thousands of lines a second would otherwise
// spend the tab's whole budget on framing.
type LogsBatch struct {
	Lines []LogLine `json:"lines,omitempty"`
	Edges []LogEdge `json:"edges,omitempty"`
	// Dropped is how many lines the server-side ring has discarded. A buffer
	// that quietly loses lines makes a chatty workload look calm.
	Dropped int `json:"dropped,omitempty"`
	// Reset marks the batch that replaces the view, which is what a fresh
	// subscription and a reconnect both send.
	Reset bool `json:"reset,omitempty"`
}

// LogLine is one merged line. Time is RFC3339Nano, or empty when the kubelet
// did not stamp it.
type LogLine struct {
	Seq       int64  `json:"seq"`
	Time      string `json:"time,omitempty"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Text      string `json:"text"`
	Previous  bool   `json:"previous,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Late      bool   `json:"late,omitempty"`
}

// LogEdge is a boundary in what the cluster can still tell us.
type LogEdge struct {
	Kind      string `json:"kind"` // horizon, restart, gone, error, ended
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	Time      string `json:"time,omitempty"`
	Reason    string `json:"reason"`
}

func logLine(l logs.Line) LogLine {
	out := LogLine{
		Seq: l.Seq, Pod: l.Pod, Container: l.Container, Text: l.Text,
		Previous: l.Previous, Truncated: l.Truncated, Late: l.Late,
	}
	if !l.Time.IsZero() {
		out.Time = l.Time.Format(time.RFC3339Nano)
	}
	return out
}

func logEdge(e logs.Edge) LogEdge {
	out := LogEdge{Kind: e.Kind, Pod: e.Pod, Container: e.Container, Reason: e.Reason}
	if !e.Time.IsZero() {
		out.Time = e.Time.Format(time.RFC3339Nano)
	}
	return out
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

package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/fleet"
	"github.com/dynaum/kubeside/internal/promotion"
)

// Fleet answers "is every cluster running the latest version" for one app.
//
// Opening this view states the intent to ask every cluster, so unlike the app
// list it deliberately wakes every context in the kubeconfig.
//
// Each context gets its own goroutine and its own copy of the timeout, the way
// cmd/kubeside's gather does, because the flag means "per-cluster connect and
// fetch". One budget spent serially would let a few slow clusters exhaust it
// and report every cluster behind them as unreachable — an invented unreachable
// on the one screen whose job is keeping unreachable honest, and one whose
// answer would change with ConnectOrder.
//
// Rows are still assembled once, when every context has answered or given up.
// Each goroutine writes to its own index rather than appending, so the view is
// built from the kubeconfig's order and never from the scheduler's.
//
// Every context is read at clusters.TierActive. This screen compares digests
// across clusters and the digest exists only in pod status, so a background
// read, which skips pods, would leave every digest permanently pending and make
// the worst thing this screen can find — one tag running as two different
// images — impossible to find. The tier is passed to the read rather than set
// on the connection: a sweep is not attention, and promoting ten contexts would
// keep them all out of the idle reaper.
func (s *Service) Fleet(app, namespace string) fleet.View {
	order := s.mgr.ConnectOrder()
	want := promotion.Identity(app, namespace)

	placements := make([]fleet.Placement, len(order))
	var wg sync.WaitGroup
	for i, name := range order {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
			defer cancel()

			placements[i] = s.placeApp(ctx, name, app, namespace, want)
		}(i, name)
	}
	wg.Wait()

	return fleet.Build(app, namespace, placements)
}

// placeApp asks one cluster about one app and always returns a row.
func (s *Service) placeApp(ctx context.Context, name, app, namespace, want string) fleet.Placement {
	kctx := s.cfg.MustGet(name)
	env := s.conf.Environment(kctx)

	// ClusterID is read from the kubeconfig cluster entry before any
	// connection attempt, on every path including the failure paths
	// below. fleet.mergeByCluster keys on this to collapse two contexts
	// aimed at one cluster; populating it from a successful connection
	// instead would leave an unreachable duplicate with an empty
	// ClusterID, which fails to merge with its reachable twin and
	// inflates Clusters in exactly the case the merge exists to prevent.
	p := fleet.Placement{
		Context:   name,
		ClusterID: config.NormalizeURL(kctx.Server),
		Env:       env.Name,
	}

	if err := s.mgr.Connect(ctx, name); err != nil {
		p.State = fleet.StateUnreachable
		p.Reason = err.Error()
		return p
	}
	client, ok := s.mgr.ClientFor(name)
	if !ok {
		p.State = fleet.StateUnreachable
		p.Reason = "not connected"
		return p
	}

	snap, err := clusters.Fetch(ctx, client, kctx, clusters.FetchOptions{Tier: clusters.TierActive})
	if err != nil {
		p.State = fleet.StateUnreachable
		p.Reason = err.Error()
		return p
	}

	match, skipped, found := findApp(snap.Apps, app, namespace, want)
	if !found {
		p.State, p.Reason = missingBecause(snap, app, namespace, want)
		return p
	}

	in := instanceOf(env.Name, match)
	p.State = fleet.StatePresent
	p.Reason = skipped
	p.Namespace = in.Namespace
	p.Image = in.Image
	p.Tag = in.Tag
	p.Digest = in.Digest
	p.DigestPending = in.Digest == ""
	p.Health = in.Health
	p.Ready = in.Ready
	p.RevisionAt = in.RevisionAt
	return p
}

// findApp matches by the same identity the promotion matrix uses, so one app is
// one app on both screens.
//
// Two namespaces in one cluster can strip to a single identity — team-a-qa and
// team-a-prod side by side, the arrangement StripEnvToken exists for — and a
// row holds one placement, so one match has to win. The namespace actually
// asked for wins; failing that, the first in name order, so the answer never
// depends on the order the snapshot happened to arrive in. The runner-up is
// returned rather than dropped: a match that disappears without a word is the
// failure this screen exists to prevent.
func findApp(list []apps.App, app, namespace, want string) (match apps.App, skipped string, found bool) {
	var matches []apps.App
	for _, a := range list {
		if promotion.Identity(a.Key.Name, a.Key.Namespace) == want {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		return apps.App{}, "", false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		exactI, exactJ := matches[i].Key.Namespace == namespace, matches[j].Key.Namespace == namespace
		if exactI != exactJ {
			return exactI
		}
		return matches[i].Key.Namespace < matches[j].Key.Namespace
	})
	if len(matches) == 1 {
		return matches[0], "", true
	}

	others := make([]string, 0, len(matches)-1)
	for _, a := range matches[1:] {
		others = append(others, a.Key.Namespace)
	}
	return matches[0], fmt.Sprintf("%d namespaces here hold this app; showing %s, not %s",
		len(matches), matches[0].Key.Namespace, strings.Join(others, ", ")), true
}

// missingBecause tells "we were not allowed to look" apart from "it is not
// here", which is the conflation this screen exists to prevent.
//
// clusters.Fetch never returns an error for an RBAC refusal: it records the
// kinds it could not read in Snapshot.Partial and returns what it got. Two
// things, and only two, turn a miss into a denial:
//
//   - A kind that could carry the app went unread. Snapshot.Partial unions two
//     different facts, a refusal and a deliberate skip, and only the first
//     implies denial — so the test is on the kinds apps.Group builds apps out
//     of. A refused Pod or ReplicaSet list hides how healthy an app is, never
//     whether it exists.
//   - The cluster refused to enumerate its namespaces, so the read fell back to
//     a single namespace. If the app lives anywhere else we never looked, which
//     is denied. If the namespace we did read is the one asked about, we looked
//     and found nothing, and absent is the honest answer.
//
// Service.Promotion is not a precedent for either line: it never inspects
// Partial, so a refused read reads as absent there. Reconciling the two screens
// is its own change, not this one.
func missingBecause(snap clusters.Snapshot, app, namespace, want string) (state, reason string) {
	if refused := refusedCarriers(snap.Partial); len(refused) > 0 {
		return fleet.StateDenied, refusedReason(refused, snap.Scope)
	}
	if !scopeCovers(snap.Scope, app, want) {
		return fleet.StateDenied, unreadNamespaceReason(snap.Scope, namespace)
	}
	return fleet.StateAbsent, ""
}

// appCarrying maps the kinds an app can be built out of to the resource names a
// Role spells them with. Pod and ReplicaSet are deliberately absent: a
// ReplicaSet whose Deployment went unread still names its owner, and a pod list
// says nothing about whether a workload exists.
var appCarrying = map[string]string{
	"Deployment":  "deployments",
	"StatefulSet": "statefulsets",
	"DaemonSet":   "daemonsets",
	"CronJob":     "cronjobs",
	"Job":         "jobs",
}

// refusedCarriers keeps only the unread kinds that could have hidden the app.
func refusedCarriers(partial []string) []string {
	out := make([]string, 0, len(partial))
	for _, kind := range partial {
		if _, ok := appCarrying[kind]; ok {
			out = append(out, kind)
		}
	}
	return out
}

// refusedReason names what to ask for, not only what failed. An RBAC message a
// developer can act on carries a verb, a resource and a scope, because that is
// the shape of a Role rule; a bare kind name is half of it and the wrong half.
func refusedReason(kinds []string, scope clusters.Scope) string {
	resources := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		resources = append(resources, appCarrying[kind])
	}
	return fmt.Sprintf("cannot read %s here; the role needs: list %s %s",
		strings.Join(kinds, ", "), strings.Join(resources, ", "), scopePhrase(scope))
}

// unreadNamespaceReason explains a miss nobody could have seen: the cluster
// refused to list its namespaces, so only the fallback namespace was read, and
// the one asked about was never opened.
func unreadNamespaceReason(scope clusters.Scope, namespace string) string {
	reason := scope.Reason
	if reason == "" {
		reason = "the namespace list was not readable"
	}
	return fmt.Sprintf("%s; only %s was read, so %s was never looked in",
		reason, strings.Join(scope.Namespaces, ", "), namespace)
}

// scopeCovers answers whether a read could have found the app at all.
//
// A cluster-wide read looked everywhere. A fallback read looked in one
// namespace, and that only counts if the app would have carried the identity we
// asked for had it been there. promotion.Identity is the rule findApp matches
// with, so asking it about the namespace we did read answers "would we have
// seen it" without guessing at environment suffixes.
func scopeCovers(scope clusters.Scope, app, want string) bool {
	if scope.ClusterWide {
		return true
	}
	for _, ns := range scope.Namespaces {
		if promotion.Identity(app, ns) == want {
			return true
		}
	}
	return false
}

func scopePhrase(scope clusters.Scope) string {
	if scope.ClusterWide || len(scope.Namespaces) == 0 {
		return "across the cluster"
	}
	return "in " + strings.Join(scope.Namespaces, ", ")
}

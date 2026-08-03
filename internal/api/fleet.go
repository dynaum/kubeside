package api

import (
	"context"
	"strings"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/fleet"
	"github.com/dynaum/kubeside/internal/promotion"
)

// Fleet answers "is every cluster running the latest version" for one app.
//
// Opening this view states the intent to ask every cluster, so unlike the app
// list it deliberately wakes every context in the kubeconfig. Rows land as
// clusters answer and a cluster behind a VPN never blocks the rest.
func (s *Service) Fleet(app, namespace string) fleet.View {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	want := promotion.Identity(app, namespace)
	var placements []fleet.Placement

	for _, name := range s.mgr.ConnectOrder() {
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
			placements = append(placements, p)
			continue
		}
		client, ok := s.mgr.ClientFor(name)
		if !ok {
			p.State = fleet.StateUnreachable
			p.Reason = "not connected"
			placements = append(placements, p)
			continue
		}

		snap, err := clusters.Fetch(ctx, client, kctx, clusters.FetchOptions{Tier: s.mgr.Tier(name)})
		if err != nil {
			p.State = fleet.StateUnreachable
			p.Reason = err.Error()
			placements = append(placements, p)
			continue
		}

		match, found := findApp(snap.Apps, want)
		if !found {
			// Not in the snapshot has two causes and they are not the same
			// fact. clusters.Fetch never errors on an RBAC refusal: it records
			// the kinds it could not read in Snapshot.Partial and returns what
			// it got. So an app missing from a snapshot with refused kinds is
			// an app we were not allowed to look for, and calling that "not
			// deployed here" is the conflation this screen exists to prevent.
			// internal/api/service.go:196 already draws the same line for the
			// apps list.
			if len(snap.Partial) > 0 || (len(snap.Scope.Namespaces) == 0 && !snap.Scope.ClusterWide) {
				p.State = fleet.StateDenied
				p.Reason = refusedReason(snap)
			} else {
				p.State = fleet.StateAbsent
			}
			placements = append(placements, p)
			continue
		}

		in := instanceOf(env.Name, match)
		p.State = fleet.StatePresent
		p.Namespace = in.Namespace
		p.Image = in.Image
		p.Tag = in.Tag
		p.Digest = in.Digest
		p.DigestPending = in.Digest == ""
		p.Health = in.Health
		p.Ready = in.Ready
		p.RevisionAt = in.RevisionAt
		placements = append(placements, p)
	}

	return fleet.Build(app, namespace, placements)
}

// findApp matches by the same identity the promotion matrix uses, so one app
// is one app on both screens.
func findApp(list []apps.App, want string) (apps.App, bool) {
	for _, a := range list {
		if promotion.Identity(a.Key.Name, a.Key.Namespace) == want {
			return a, true
		}
	}
	return apps.App{}, false
}

// refusedReason names what the cluster would not let us read, so a denied row
// says which verb to ask for rather than just refusing to answer.
func refusedReason(snap clusters.Snapshot) string {
	if len(snap.Partial) > 0 {
		return "could not read " + strings.Join(snap.Partial, ", ")
	}
	if snap.Scope.Reason != "" {
		return snap.Scope.Reason
	}
	return "no readable namespace"
}

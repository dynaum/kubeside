package api

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/exec"
	"github.com/dynaum/kubeside/internal/forward"
	"github.com/dynaum/kubeside/internal/guard"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/logs"
	"github.com/dynaum/kubeside/internal/metrics"
	"github.com/dynaum/kubeside/internal/promotion"
	"github.com/dynaum/kubeside/internal/rbac"
	"github.com/dynaum/kubeside/internal/resolved"
	"github.com/dynaum/kubeside/internal/session"
	"github.com/dynaum/kubeside/internal/timeline"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

func errUnknownContext(name string) error {
	return fmt.Errorf("unknown context %q", name)
}

// Service implements API over the cluster manager. It connects lazily and
// fetches on demand, holding nothing on disk.
type Service struct {
	cfg     *kubeconfig.Config
	mgr     *clusters.Manager
	conf    *config.Config
	opts    kubeconfig.Options
	timeout time.Duration
	// timelines memoizes reconstruction, which reads several collections per
	// call and is triggered by opening a screen.
	timelines *memo[TimelineView]
	// startedAt is when this process began watching. The timeline marks it
	// whether or not anything has happened since: "we were watching and
	// nothing changed" and "we have no idea" are different answers, and the
	// screen must be able to tell them apart.
	startedAt time.Time
	// snaps retains the last successful read per context, which is what a
	// stale connection renders instead of an empty cluster.
	snapsMu sync.Mutex
	snaps   map[string]clusters.Snapshot
	// guard is the ceremony between a developer and a destructive action. It
	// protects against accidents; the permission check beside it is the
	// security boundary.
	guard *guard.Guard
	// perms answers what this reader may do, per context, cached for the
	// session. Every disabled control in the UI is disabled because a cluster
	// said so.
	perms *rbac.Resolver
	// exec runs interactive shells. It is the one genuinely destructive thing
	// this product does, which is why it goes through the guard and the
	// permission resolver before a session opens.
	exec *exec.Session
	// forwards owns every live port-forward. They die with the process, like
	// everything else kubeside holds.
	forwards *forward.Manager
	// live holds what kubeside watched happen while it was running. It is
	// merged with reconstruction rather than replacing it: one is what the
	// cluster remembers, the other is what we saw.
	live *session.Store
}

// NewService builds the API backend. conf may be nil, which is the zero-config
// first run: environments are then inferred from context names.
func NewService(cfg *kubeconfig.Config, mgr *clusters.Manager, opts kubeconfig.Options, conf *config.Config, timeout time.Duration) *Service {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	if conf == nil {
		conf = config.Empty()
	}
	return &Service{
		cfg: cfg, mgr: mgr, conf: conf, opts: opts, timeout: timeout,
		timelines: newMemo[TimelineView](memoTTL),
		live:      session.New(session.Limits{}),
		startedAt: time.Now(),
		snaps:     map[string]clusters.Snapshot{},
		guard:     guard.New(nil),
		perms:     rbac.New(rbac.KubeReviewer{ClientFor: mgr.ClientFor}),
		forwards: forward.New(forward.KubeTunnel{
			RESTConfig: func(contextName string) (*rest.Config, error) {
				return kubeconfig.RESTConfigFor(opts, contextName)
			},
		}),
	}
}

// Contexts returns every context with its live connection state, in connect
// order so the current context leads.
func (s *Service) Contexts() []ContextView {
	statuses := s.mgr.Statuses()
	out := make([]ContextView, 0, len(statuses))
	for _, st := range statuses {
		kctx, _ := s.cfg.Get(st.Context)
		env := s.conf.Environment(kctx)
		v := ContextView{
			Name:        st.Context,
			Current:     kctx.IsCurrent,
			State:       st.State.String(),
			HasData:     st.State.HasData(),
			Environment: env.Name,
			Risk:        env.Risk.String(),
			Color:       env.Color,
			Hazard:      env.Hazard,
			Write:       env.Write.String(),
		}
		if st.Age > 0 {
			v.AgeSec = int64(st.Age.Seconds())
		}
		if st.Err != nil {
			v.Error = st.Err.Error()
		}
		out = append(out, v)
	}
	return out
}

// Apps connects the named context if needed, fetches, and renders the view.
//
// An unreached context returns its state and no app list, never an empty one:
// absence of knowledge and absence of apps are different facts.
func (s *Service) Apps(name string) (AppsView, error) {
	if _, ok := s.cfg.Get(name); !ok {
		return AppsView{}, errUnknownContext(name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	connErr := s.mgr.Connect(ctx, name)
	s.mgr.SetTier(name, clusters.TierActive)
	s.mgr.Touch(name)
	st, _ := s.mgr.Status(name)

	if !st.State.HasData() {
		view := AppsView{Context: name, State: st.State.String(), Apps: []AppView{}}
		if connErr != nil {
			view.Error = connErr.Error()
		}
		return view, nil
	}

	// An idle-reaped context keeps its last snapshot, which renders with its
	// state rather than as an empty cluster.
	if st.State == clusters.StateStale {
		if last, ok := s.lastSnapshot(name); ok {
			return AppsFromSnapshot(last, st.State.String(), s.metricsInfo(ctx, name)), nil
		}
	}

	client, ok := s.mgr.ClientFor(name)
	if !ok {
		return AppsView{Context: name, State: st.State.String(), Apps: []AppView{}}, nil
	}

	snap, err := clusters.Fetch(ctx, client, s.cfg.MustGet(name), clusters.FetchOptions{Tier: clusters.TierActive})
	if err != nil {
		// A failed read does not erase what was last known. The old answer with
		// its state beats an empty one.
		if last, ok := s.lastSnapshot(name); ok {
			view := AppsFromSnapshot(last, st.State.String(), s.metricsInfo(ctx, name))
			view.Error = err.Error()
			return view, nil
		}
		return AppsView{Context: name, State: st.State.String(), Error: err.Error(), Apps: []AppView{}}, nil
	}

	// A read where every kind failed produces an empty list that is
	// indistinguishable from an empty cluster. What was last known beats that,
	// with the failures named.
	if len(snap.Apps) == 0 && len(snap.Partial) > 0 {
		if last, ok := s.lastSnapshot(name); ok {
			view := AppsFromSnapshot(last, st.State.String(), s.metricsInfo(ctx, name))
			view.Partial = snap.Partial
			view.Reason = "nothing could be read just now; showing what was last known"
			return view, nil
		}
	}

	s.remember(name, snap)
	return AppsFromSnapshot(snap, st.State.String(), s.metricsInfo(ctx, name)), nil
}

// metricsInfo probes the metrics source so the UI knows whether to render usage
// columns. It never fabricates a reading: an unavailable source reports why.
//
// A config that names a source overrides the probe, including "none", which
// switches the usage columns off rather than rendering zeroes.
func (s *Service) metricsInfo(ctx context.Context, name string) MetricsInfo {
	if src := s.conf.Defaults.Metrics; src == "none" {
		return MetricsInfo{Source: "none", Available: false, Reason: "disabled in config"}
	}
	client, ok := s.mgr.ClientFor(name)
	if !ok {
		return MetricsInfo{Source: "none", Available: false, Reason: "not connected"}
	}
	var mc metrics.PodMetricsAPI
	if rc, err := kubeconfig.RESTConfigFor(s.opts, name); err == nil {
		if cs, err := metricsv.NewForConfig(rc); err == nil {
			mc = metrics.FromClientset(cs)
		}
	}
	src := metrics.Probe(client.Discovery(), mc, "")
	return MetricsInfo{Source: src.Name(), Available: src.Available(), Reason: src.Unavailable()}
}

// LogSource opens the log side of one workload.
//
// The pod set comes from the grouping engine, not a label selector: the engine
// already decided which pods are this app, and a selector would be a second
// answer that disagrees on exactly the workloads that need it most.
func (s *Service) LogSource(contextName, namespace, workload string) (logs.Source, error) {
	if _, ok := s.cfg.Get(contextName); !ok {
		return nil, errUnknownContext(contextName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(ctx, contextName); err != nil {
		return nil, err
	}
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return nil, fmt.Errorf("context %q is not connected", contextName)
	}

	return logs.KubeSource{
		Client:    client,
		Namespace: namespace,
		PodNames: func(ctx context.Context) ([]string, error) {
			return s.podsOf(ctx, contextName, namespace, workload)
		},
	}, nil
}

// podsOf resolves the pods currently backing a workload.
//
// It re-reads the cluster rather than trusting a list captured at subscribe
// time, which is what lets a rollout's new replicas join a stream already on
// screen. The read is a full snapshot today; informer-backed watches (#23)
// make it cheap without changing this contract.
func (s *Service) podsOf(ctx context.Context, contextName, namespace, workload string) ([]string, error) {
	app, err := s.appOf(ctx, contextName, namespace, workload)
	if err != nil {
		return nil, err
	}
	return podNames(app), nil
}

// Timeline reconstructs one workload's history from the cluster.
//
// Nothing here was recorded by kubeside: every entry is assembled on demand
// from what Kubernetes still holds, which is why two developers opening the
// same app see the same history.
func (s *Service) Timeline(contextName, namespace, workload string) (TimelineView, error) {
	if _, ok := s.cfg.Get(contextName); !ok {
		return TimelineView{}, errUnknownContext(contextName)
	}
	key := sessionKey(contextName, namespace, workload)
	view, err := s.timelines.Do(key, func() (TimelineView, error) {
		return s.reconstruct(contextName, namespace, workload)
	})
	if err != nil {
		return TimelineView{}, err
	}

	// Reconstruction is memoized; live observations are not. Merging them on
	// every call is what keeps a change that happened thirty seconds ago from
	// waiting for a cache to expire.
	view.Entries = session.Merge(view.Entries, s.live.Entries(key))
	view.Entries = session.Merge(view.Entries, s.live.Entries(auditKey(contextName)))

	// The session marker is mandatory. An app kubeside has watched all along
	// without seeing a change still gets one, because an unmarked lane reads as
	// "nothing is known" when the truth is "nothing happened".
	view.Horizons = append(append([]timeline.Horizon{}, view.Horizons...), s.sessionHorizon(key))
	return view, nil
}

// Observed records a row that changed between two reads.
//
// Only health transitions are kept. Replica counts churn through every rollout
// and a timeline of them buries the one line that matters, which is the moment
// an app stopped being healthy.
func (s *Service) Observed(contextName string, before, after AppView) {
	if before.Health == "" || before.Health == after.Health {
		return
	}
	key := sessionKey(contextName, after.Namespace, after.Name)
	s.live.Record(key, timeline.Entry{
		At:     time.Now(),
		Kind:   timeline.KindHealth,
		Title:  before.Health + " → " + after.Health,
		Detail: after.Detail,
		Source: "session",
	})
}

// sessionHorizon is where this app's live history begins. The buffer answers
// when it holds something; otherwise the answer is when kubeside started.
func (s *Service) sessionHorizon(key string) timeline.Horizon {
	if h := s.live.Horizon(key); h != nil {
		return *h
	}
	return timeline.Horizon{
		At:     s.startedAt,
		Source: "session",
		Pruned: false,
		Reason: "kubeside started watching here; anything before this comes from the cluster's own history",
	}
}

func sessionKey(contextName, namespace, workload string) string {
	return contextName + "|" + namespace + "/" + workload
}

// auditKey is where events about an environment live, as opposed to events
// about one app in it.
//
// Arming production is not a fact about a workload, and filing it under one
// meant filing it under whatever namespace and workload the caller happened to
// send — empty, for the dialog that arms an environment, which put the record
// somewhere no timeline reads. These entries are merged into every timeline in
// the context instead, because every app in that environment was affected.
func auditKey(contextName string) string {
	return contextName + "|environment"
}

func (s *Service) reconstruct(contextName, namespace, workload string) (TimelineView, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	if err := s.mgr.Connect(ctx, contextName); err != nil {
		return TimelineView{}, err
	}
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return TimelineView{}, fmt.Errorf("context %q is not connected", contextName)
	}

	app, err := s.appOf(ctx, contextName, namespace, workload)
	if err != nil {
		return TimelineView{}, err
	}

	tl := timeline.Reconstruct(ctx, client, timeline.Target{
		Namespace: namespace,
		Name:      workload,
		Kind:      app.Kind,
		UID:       types.UID(ownerUID(app)),
		Pods:      podNames(app),
	})

	return TimelineView{
		Context:   contextName,
		Namespace: namespace,
		Workload:  workload,
		Entries:   tl.Entries,
		Horizons:  tl.Horizons,
		Gaps:      tl.Gaps,
	}, nil
}

// appOf finds one app in the current snapshot. The grouping engine already
// decided what an app is; the timeline asks it rather than guessing again.
func (s *Service) appOf(ctx context.Context, contextName, namespace, workload string) (apps.App, error) {
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return apps.App{}, fmt.Errorf("context %q is not connected", contextName)
	}
	snap, err := clusters.Fetch(ctx, client, s.cfg.MustGet(contextName), clusters.FetchOptions{Tier: clusters.TierActive})
	if err != nil {
		return apps.App{}, err
	}
	for _, a := range snap.Apps {
		if a.Key.Namespace == namespace && a.Key.Name == workload {
			return a, nil
		}
	}
	return apps.App{}, fmt.Errorf("no workload %q in namespace %q of context %q", workload, namespace, contextName)
}

// ownerUID is the UID of the app's primary workload, which is how history is
// attributed to this app and not to a same-named one that was deleted and
// recreated.
func ownerUID(a apps.App) string {
	for _, w := range a.Workloads {
		if w.Kind == a.Kind && w.Name == a.Key.Name {
			return w.UID
		}
	}
	return ""
}

func podNames(a apps.App) []string {
	var out []string
	for _, w := range a.Workloads {
		if w.Kind == "Pod" {
			out = append(out, w.Name)
		}
	}
	return out
}

// AppDetail is everything Screen 2 needs in one read.
//
// The current state comes from the grouping engine, the history from
// reconstruction, and the running image from the newest rollout, which is the
// only place it survives without a second read.
func (s *Service) AppDetail(contextName, namespace, workload string) (AppDetailView, error) {
	if _, ok := s.cfg.Get(contextName); !ok {
		return AppDetailView{}, errUnknownContext(contextName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(ctx, contextName); err != nil {
		return AppDetailView{}, err
	}

	app, err := s.appOf(ctx, contextName, namespace, workload)
	if err != nil {
		return AppDetailView{}, err
	}

	tl, err := s.Timeline(contextName, namespace, workload)
	if err != nil {
		return AppDetailView{}, err
	}

	h := apps.Assess(app)
	view := AppDetailView{
		Context:   contextName,
		Namespace: namespace,
		Workload:  workload,
		Kind:      app.Kind,
		Health:    h.Health.String(),
		Reason:    h.Reason,
		Detail:    h.Detail,
		Ready:     readyRatio(app),
		Pods:      PodsOf(app, time.Now()),
		Timeline:  tl,
	}
	for _, p := range view.Pods {
		view.Restarts += p.Restarts
	}
	if e, ok := newestRollout(tl.Entries); ok {
		view.Image = e.Image
		view.RevisionAt = e.At.UTC().Format(time.RFC3339)
	}
	return view, nil
}

// newestRollout finds the most recent deploy, which is what the app is running
// now.
func newestRollout(entries []timeline.Entry) (timeline.Entry, bool) {
	for _, e := range entries {
		if e.Kind == timeline.KindDeploy {
			return e, true
		}
	}
	return timeline.Entry{}, false
}

// Config resolves what one container actually received.
//
// The reading comes from a running pod rather than the workload's template,
// because the template is what was asked for and the pod is what happened. A
// rollout that has not finished makes those different, and the difference is
// the thing worth seeing.
func (s *Service) Config(contextName, namespace, workload string) (ConfigView, error) {
	if _, ok := s.cfg.Get(contextName); !ok {
		return ConfigView{}, errUnknownContext(contextName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(ctx, contextName); err != nil {
		return ConfigView{}, err
	}
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return ConfigView{}, fmt.Errorf("context %q is not connected", contextName)
	}

	app, err := s.appOf(ctx, contextName, namespace, workload)
	if err != nil {
		return ConfigView{}, err
	}
	name := representativePod(app)
	if name == "" {
		return ConfigView{}, fmt.Errorf("%s has no pods; there is no resolved configuration to read", workload)
	}

	pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ConfigView{}, err
	}

	cfg := resolved.ResolveWith(ctx, client, pod, func(secret string) (bool, string) {
		return s.mayGetSecret(ctx, contextName, namespace, secret)
	})

	// The previous revision's pod template is still on the cluster inside its
	// ReplicaSet, which is the only place old configuration survives at all.
	if prev, revision, err := s.previousTemplate(ctx, client, namespace, app); err == nil {
		resolved.Compare(&cfg, prev, revision)
	}

	return ConfigView{
		Context:    contextName,
		Namespace:  namespace,
		Workload:   workload,
		Pod:        cfg.Pod,
		Containers: cfg.Containers,
		Caveat:     cfg.Caveat,
		ComparedTo: cfg.ComparedTo,
	}, nil
}

// representativePod picks the pod to read configuration from: a ready one when
// there is one, because a crash-looping pod may never have resolved its
// environment at all.
// hasPod reports whether the pod belongs to this app's group.
func hasPod(a apps.App, name string) bool {
	for _, w := range a.Workloads {
		if w.Kind == "Pod" && w.Name == name {
			return true
		}
	}
	return false
}

func representativePod(a apps.App) string {
	var fallback string
	for _, w := range a.Workloads {
		if w.Kind != "Pod" {
			continue
		}
		if fallback == "" {
			fallback = w.Name
		}
		if w.Status != nil && w.Status.Ready {
			return w.Name
		}
	}
	return fallback
}

// mayGetSecret asks whether this reader may get one specific Secret.
//
// It goes through the shared resolver, so the answer is cached for the session
// and the same question asked by the configuration screen, the diff, and the
// reveal costs one round trip rather than three.
func (s *Service) mayGetSecret(ctx context.Context, contextName, namespace, secret string) (bool, string) {
	p := s.perms.Can(ctx, contextName, rbac.Action{
		Verb: "get", Resource: "secrets", Name: secret, Namespace: namespace,
	})
	return p.Allowed, p.Reason
}

// Can answers one permission question for the UI, which is how a control knows
// whether to render itself disabled and what verb to name.
func (s *Service) Can(contextName string, action rbac.Action) rbac.Permission {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(ctx, contextName); err != nil {
		return rbac.Permission{Reason: fmt.Sprintf("could not check permission to %s: %v", action.Key(), err)}
	}
	return s.perms.Can(ctx, contextName, action)
}

// RevealSecret fetches one key of one Secret, on demand.
//
// Three things make this safe enough to exist. The permission is checked
// against the specific Secret, not inferred. Only the requested key is
// returned, never the whole object. And every reveal is written to the session
// timeline, so reading a production credential leaves a trace in the same place
// every other change does.
func (s *Service) RevealSecret(contextName, namespace, secret, key, workload string) (RevealView, error) {
	if _, ok := s.cfg.Get(contextName); !ok {
		return RevealView{}, errUnknownContext(contextName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(ctx, contextName); err != nil {
		return RevealView{}, err
	}
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return RevealView{}, fmt.Errorf("context %q is not connected", contextName)
	}

	if allowed, reason := s.mayGetSecret(ctx, contextName, namespace, secret); !allowed {
		return RevealView{}, &ForbiddenError{Reason: reason}
	}

	obj, err := client.CoreV1().Secrets(namespace).Get(ctx, secret, metav1.GetOptions{})
	if err != nil {
		return RevealView{}, err
	}
	raw, ok := obj.Data[key]
	if !ok {
		return RevealView{}, fmt.Errorf("secret %s has no key %q", secret, key)
	}

	// The reveal is recorded before the value is returned, so a value that
	// reached a screen is always a value the timeline knows about.
	s.recordReveal(contextName, namespace, workload, secret, key)

	// client-go decodes base64 already. Nobody should have to run base64 -d to
	// read their own configuration.
	if !utf8.Valid(raw) {
		return RevealView{
			Secret: secret, Key: key, Binary: true,
			Note: fmt.Sprintf("%d bytes of binary data, not text", len(raw)),
		}, nil
	}
	return RevealView{Secret: secret, Key: key, Value: string(raw)}, nil
}

// recordReveal writes the reveal to the session timeline. The value never
// appears in the entry: an audit trail that leaks what it audits is worse than
// none.
func (s *Service) recordReveal(contextName, namespace, workload, secret, key string) {
	// The workload is caller-supplied, so an absent one must not be able to
	// drop the record. A reveal that reached a screen is always on a timeline.
	where := auditKey(contextName)
	if workload != "" {
		where = sessionKey(contextName, namespace, workload)
	}
	s.live.Record(where, timeline.Entry{
		At:     time.Now(),
		Kind:   timeline.KindReveal,
		Title:  "revealed " + key,
		Detail: "from secret " + secret,
		Source: "session",
		Actor:  timeline.Actor{Kind: timeline.ActorKubectl, Name: "this kubeside session"},
	})
}

// ForbiddenError is a refusal the transport turns into a 403 with the verb
// named, rather than a generic failure.
type ForbiddenError struct{ Reason string }

func (e *ForbiddenError) Error() string { return e.Reason }

// previousTemplate finds the pod template of the revision before the current
// one.
//
// Only Deployments keep their history this way. For anything else the answer is
// that there is no previous template to compare against, which the diff column
// renders as no claim rather than as "unchanged".
func (s *Service) previousTemplate(ctx context.Context, client kubernetes.Interface, namespace string, app apps.App) (resolved.Snapshot, string, error) {
	if app.Kind != "Deployment" {
		return resolved.Snapshot{}, "", fmt.Errorf("%s keeps no pod-template history", app.Kind)
	}

	list, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return resolved.Snapshot{}, "", err
	}

	owner := types.UID(ownerUID(app))
	type rev struct {
		n  int
		rs appsv1.ReplicaSet
	}
	var mine []rev
	for _, rs := range list.Items {
		if !ownedBy(rs.OwnerReferences, owner) {
			continue
		}
		n, err := strconv.Atoi(rs.Annotations["deployment.kubernetes.io/revision"])
		if err != nil {
			continue
		}
		mine = append(mine, rev{n: n, rs: rs})
	}
	if len(mine) < 2 {
		return resolved.Snapshot{}, "", fmt.Errorf("no previous revision on the cluster")
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].n > mine[j].n })

	prev := mine[1]
	return resolved.FromTemplate(
		prev.rs.Spec.Template.Spec.Containers,
		prev.rs.Spec.Template.Spec.InitContainers,
	), strconv.Itoa(prev.n), nil
}

func ownedBy(refs []metav1.OwnerReference, owner types.UID) bool {
	for _, r := range refs {
		if r.UID == owner {
			return true
		}
	}
	return false
}

// Diff compares one app's configuration across two environments.
//
// Both sides are resolved the same way the configuration screen resolves one,
// then classified. Secret values are hashed inside this process and never
// returned: the answer says two credentials differ without either appearing on
// a screen.
func (s *Service) Diff(req DiffRequest) (DiffView, error) {
	left, err := s.Config(req.Context, req.Namespace, req.Workload)
	if err != nil {
		return DiffView{}, fmt.Errorf("%s: %w", req.Context, err)
	}

	// A shared config may map the app to a different namespace or name per
	// environment. That mapping is exactly what apps.match in config.yaml is
	// for, so it is consulted before falling back to the same coordinates.
	rightNs, rightName := req.OtherNamespace, req.OtherWorkload
	if ref, ok := s.conf.AppRef(req.Workload, s.environmentName(req.Other)); ok {
		if rightNs == "" {
			rightNs = ref.Namespace
		}
		if rightName == "" {
			rightName = ref.Name
		}
	}
	if rightNs == "" {
		rightNs = req.Namespace
	}
	if rightName == "" {
		rightName = req.Workload
	}

	right, err := s.Config(req.Other, rightNs, rightName)
	if err != nil {
		return DiffView{}, fmt.Errorf("%s: %w", req.Other, err)
	}

	leftContainer, ok := pickContainer(left.Containers, req.Container)
	if !ok {
		return DiffView{}, fmt.Errorf("no container to compare in %s", req.Workload)
	}
	rightContainer, ok := pickContainer(right.Containers, leftContainer.Name)
	if !ok {
		return DiffView{}, fmt.Errorf("%s has no container %q", req.Other, leftContainer.Name)
	}

	s.fillDigests(req.Context, req.Namespace, &leftContainer)
	s.fillDigests(req.Other, rightNs, &rightContainer)

	leftEnv := s.envOf(req.Context)
	rightEnv := s.envOf(req.Other)
	rows := resolved.CrossDiff(leftContainer, rightContainer, leftEnv, rightEnv)

	return DiffView{
		Left:      DiffSide{Context: req.Context, Namespace: req.Namespace, Workload: req.Workload, Env: leftEnv, Pod: left.Pod},
		Right:     DiffSide{Context: req.Other, Namespace: rightNs, Workload: rightName, Env: rightEnv, Pod: right.Pod},
		Container: leftContainer.Name,
		Rows:      rows,
		Summary:   resolved.Summarize(rows),
	}, nil
}

// fillDigests hashes the secret values this reader is allowed to read. A Secret
// nobody may read yields no digest, and the row then makes no claim rather than
// implying the two sides match.
func (s *Service) fillDigests(contextName, namespace string, c *resolved.Container) {
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	cache := map[string]*corev1.Secret{}
	for i := range c.Values {
		v := &c.Values[i]
		if !v.Masked || v.Source.Ref == "" || v.Source.Key == "" {
			continue
		}
		if allowed, _ := s.mayGetSecret(ctx, contextName, namespace, v.Source.Ref); !allowed {
			continue
		}
		obj, ok := cache[v.Source.Ref]
		if !ok {
			got, err := client.CoreV1().Secrets(namespace).Get(ctx, v.Source.Ref, metav1.GetOptions{})
			if err != nil {
				continue
			}
			obj = got
			cache[v.Source.Ref] = got
		}
		if raw, ok := obj.Data[v.Source.Key]; ok {
			// The value is hashed here and discarded. It never enters a view.
			v.Digest = resolved.Digest(raw)
		}
	}
}

func (s *Service) envOf(contextName string) resolved.Env {
	kctx, _ := s.cfg.Get(contextName)
	env := s.conf.Environment(kctx)
	return resolved.Env{Name: env.Name, Risk: env.Risk.String()}
}

func (s *Service) environmentName(contextName string) string {
	return s.envOf(contextName).Name
}

// pickContainer chooses the container to compare: the named one, or the first
// application container, which is what a developer means by "the app".
func pickContainer(containers []resolved.Container, name string) (resolved.Container, bool) {
	for _, c := range containers {
		if c.Name == name {
			return c, true
		}
	}
	if name != "" {
		return resolved.Container{}, false
	}
	for _, c := range containers {
		if !c.Init {
			return c, true
		}
	}
	return resolved.Container{}, false
}

// StartForward opens a port-forward into one workload.
//
// The pod is chosen the same way the configuration screen chooses one: a ready
// replica, because forwarding into a crash-looping pod produces a tunnel that
// closes as fast as it opens.
func (s *Service) StartForward(req ForwardRequest) (forward.Forward, error) {
	if _, ok := s.cfg.Get(req.Context); !ok {
		return forward.Forward{}, errUnknownContext(req.Context)
	}
	if req.RemotePort <= 0 {
		return forward.Forward{}, fmt.Errorf("a container port is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(ctx, req.Context); err != nil {
		return forward.Forward{}, err
	}

	// The security boundary first, exactly as the exec path does it. A tunnel
	// is a write: it opens a socket into a cluster, and the browser disabling
	// the button is not a control, because anything holding the session token
	// can post the request the button would have sent.
	perm := s.perms.Can(ctx, req.Context, rbac.Action{
		Verb: "create", Resource: "pods", Subresource: "portforward", Namespace: req.Namespace,
	})
	if !perm.Allowed {
		return forward.Forward{}, &ForbiddenError{Reason: perm.Reason}
	}

	// The pod is the scope, not a label. A caller who names a pod outside the
	// workload is reaching for something they never opened, so the workload's
	// own group is what a named pod has to be found in.
	app, err := s.appOf(ctx, req.Context, req.Namespace, req.Workload)
	if err != nil {
		return forward.Forward{}, err
	}
	pod := req.Pod
	if pod == "" {
		if pod = representativePod(app); pod == "" {
			return forward.Forward{}, fmt.Errorf("%s has no pods to forward to", req.Workload)
		}
	} else if !hasPod(app, pod) {
		return forward.Forward{}, fmt.Errorf(
			"pod %s is not part of %s in %s; a forward reaches one workload's pods, not any pod in the namespace",
			pod, req.Workload, req.Namespace)
	}

	// Then the guardrail. Forwarding a prod database to a laptop is the write
	// that looks most like a read, which is precisely why it needs the same
	// ceremony as the ones that look dangerous.
	kctx, _ := s.cfg.Get(req.Context)
	gate := s.guard.Check(req.Context, s.conf.Environment(kctx), guard.Action{
		Verb: "port-forward", Resource: "pod", Name: app.Key.Name, Namespace: req.Namespace,
		Kubectl: fmt.Sprintf("kubectl port-forward %s %d:%d -n %s --context %s",
			pod, req.LocalPort, req.RemotePort, req.Namespace, req.Context),
		Blast: guard.Blast{Pods: 1, Summary: "one port of " + pod},
	})
	if !gate.Permitted {
		return forward.Forward{}, &ForbiddenError{Reason: gate.Reason}
	}
	if err := confirmed(gate, req.Confirm); err != nil {
		return forward.Forward{}, err
	}

	env := s.envOf(req.Context)
	return s.forwards.Start(ctx, forward.Target{
		Context: req.Context, Namespace: req.Namespace, Workload: req.Workload,
		Pod: pod, RemotePort: req.RemotePort, LocalPort: req.LocalPort,
		Environment: env.Name, Risk: env.Risk,
	})
}

// Forwards lists every live tunnel.
func (s *Service) Forwards() []forward.Forward { return s.forwards.List() }

// StopForward closes one tunnel.
func (s *Service) StopForward(id string) error { return s.forwards.Stop(id) }

// Close releases everything the service holds. No tunnel outlives the process
// that opened it.
func (s *Service) Close() { s.forwards.StopAll() }

// Promotion compares every app across every environment.
//
// Opening this view is what connects the environments: the matrix cannot answer
// "is the fix in prod yet" without reading prod. An environment that will not
// connect becomes a column of unreadable cells naming the reason, never an
// empty column that reads as "nothing is deployed there".
func (s *Service) Promotion() PromotionView {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var envs []promotion.Env
	var instances []promotion.Instance
	var unreachable []string
	seen := map[string]bool{}

	for _, name := range s.mgr.ConnectOrder() {
		kctx := s.cfg.MustGet(name)
		env := s.conf.Environment(kctx)
		// Two contexts bound to one environment would produce two identical
		// columns; the first one wins, which is what the binding is for.
		if seen[env.Name] {
			continue
		}
		seen[env.Name] = true
		envs = append(envs, promotion.Env{Name: env.Name, Risk: env.Risk.String(), Context: name})

		reason := ""
		if err := s.mgr.Connect(ctx, name); err != nil {
			reason = err.Error()
		}
		client, ok := s.mgr.ClientFor(name)
		if !ok {
			unreachable = append(unreachable, env.Name)
			instances = append(instances, promotion.Instance{
				Env: env.Name, App: "", Denied: true,
				DeniedReason: orElseString(reason, "not connected"),
			})
			continue
		}

		snap, err := clusters.Fetch(ctx, client, kctx, clusters.FetchOptions{Tier: s.mgr.Tier(name)})
		if err != nil {
			unreachable = append(unreachable, env.Name)
			continue
		}
		for _, a := range snap.Apps {
			instances = append(instances, instanceOf(env.Name, a))
		}
	}

	// Instances with no app name are placeholders for an environment nobody
	// could read; they gave us the column and nothing else.
	real := instances[:0]
	for _, in := range instances {
		if in.App != "" {
			real = append(real, in)
		}
	}

	rows := promotion.Build(envs, real)
	return PromotionView{Envs: envs, Rows: rows, Summary: promotion.Summarize(rows), Unreachable: unreachable}
}

// instanceOf reads one app's version from the snapshot.
//
// The tag comes from the workload's own image and is available immediately. The
// digest lives in pod status and is filled from the pods already in the
// snapshot; when there are none, the cell says the digest is pending rather
// than claiming the images match.
func instanceOf(env string, a apps.App) promotion.Instance {
	in := promotion.Instance{
		Env: env, App: a.Key.Name, Namespace: a.Key.Namespace, Present: true,
		Ready: readyRatio(a),
	}
	in.Health = apps.Assess(a).Health.String()

	for _, w := range a.Workloads {
		if w.Status == nil {
			continue
		}
		if w.Kind == a.Kind && w.Status.Image != "" && in.Image == "" {
			in.Image = w.Status.Image
		}
		if w.Kind == "Pod" {
			if in.Image == "" && w.Status.Image != "" {
				in.Image = w.Status.Image
			}
			if in.Digest == "" {
				in.Digest = digestOf(w.Status.ImageID)
			}
		}
		if !w.Created.IsZero() && (in.RevisionAt == "" || w.Created.After(mustTime(in.RevisionAt))) {
			in.RevisionAt = w.Created.UTC().Format(time.RFC3339)
		}
	}
	in.Tag = promotion.TagOf(in.Image)
	return in
}

// digestOf keeps the sha from an imageID, which is the part that identifies the
// code. The registry prefix in front of it varies by node and says nothing.
func digestOf(imageID string) string {
	if at := strings.LastIndex(imageID, "@"); at >= 0 {
		return imageID[at+1:]
	}
	if at := strings.Index(imageID, "sha256:"); at >= 0 {
		return imageID[at:]
	}
	return ""
}

func mustTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

func orElseString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// The actions the UI renders controls for. Named here so one list drives both
// the permission check and the buttons, and a control cannot quietly appear
// without a permission behind it.
var uiActions = []rbac.Action{
	{Verb: "create", Resource: "pods", Subresource: "exec"},
	{Verb: "create", Resource: "pods", Subresource: "portforward"},
	{Verb: "delete", Resource: "pods"},
	{Verb: "patch", Resource: "deployments", Group: "apps"},
	{Verb: "get", Resource: "pods", Subresource: "log"},
}

// Capabilities resolves every control's permission for one namespace at once.
func (s *Service) Capabilities(contextName, namespace string) CapabilitiesView {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	out := CapabilitiesView{Context: contextName, Namespace: namespace, Can: map[string]rbac.Permission{}}
	if err := s.mgr.Connect(ctx, contextName); err != nil {
		// An unreachable cluster grants nothing, and the reason says the check
		// failed rather than that access was refused.
		for _, a := range uiActions {
			a.Namespace = namespace
			out.Can[a.Key()] = rbac.Permission{
				Reason: fmt.Sprintf("could not check permission to %s: %v", a.Key(), err),
			}
		}
		return out
	}

	actions := make([]rbac.Action, 0, len(uiActions))
	for _, a := range uiActions {
		a.Namespace = namespace
		actions = append(actions, a)
	}
	out.Can = s.perms.CanAll(ctx, contextName, actions)
	return out
}

// Snapshot retention. kubeside writes nothing to disk, so "retained" means one
// snapshot per context in memory, replaced on every successful read and dropped
// with the process.
func (s *Service) remember(contextName string, snap clusters.Snapshot) {
	s.snapsMu.Lock()
	defer s.snapsMu.Unlock()
	s.snaps[contextName] = snap
}

func (s *Service) lastSnapshot(contextName string) (clusters.Snapshot, bool) {
	s.snapsMu.Lock()
	defer s.snapsMu.Unlock()
	snap, ok := s.snaps[contextName]
	return snap, ok
}

// Reap drops connections nobody has looked at, which is the idle tier.
//
// The connection goes; the snapshot stays. A context that has been idle for
// fifteen minutes renders its last known answer labelled with its age, which is
// a different thing from a context nothing is known about.
func (s *Service) Reap() []string {
	// Demote before reaping: an environment nobody has looked at for a couple
	// of minutes is background, and only after the full idle window does it
	// lose its connection entirely.
	for _, name := range s.mgr.Untouched(activeWindow) {
		s.mgr.SetTier(name, clusters.TierBackground)
	}

	reaped := s.mgr.ReapIdle()
	for _, name := range reaped {
		s.mgr.SetTier(name, clusters.TierBackground)
		// New credentials on reconnect can mean different permissions.
		s.perms.Forget(name)
	}
	return reaped
}

// StartJanitor reaps idle connections until ctx is cancelled. Nothing else in
// the process is on a timer: attention drives everything, and this is what
// notices the absence of attention.
func (s *Service) StartJanitor(ctx context.Context) {
	go func() {
		t := time.NewTicker(janitorInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Reap()
			}
		}
	}()
}

// janitorInterval is how often idleness is checked. Far shorter than the idle
// window itself, so a context is reaped near when it should be rather than up
// to a window late.
const janitorInterval = time.Minute

// activeWindow is how long a context stays active after the last read. Long
// enough to survive a developer reading a screen, short enough that a window
// left open on a stale tab stops costing pod reads.
const activeWindow = 2 * time.Minute

// Gate answers what an action costs in one environment, and whether the cluster
// would allow it at all.
//
// Both halves travel together on purpose. The guardrail is ergonomics against
// acting in the wrong window; the permission is the security boundary. A UI
// that shows one without the other teaches the wrong lesson about which is
// which.
func (s *Service) Gate(req GateRequest) (GateView, error) {
	kctx, ok := s.cfg.Get(req.Context)
	if !ok {
		return GateView{}, errUnknownContext(req.Context)
	}
	env := s.conf.Environment(kctx)

	if req.Unlock != "" {
		rec, err := s.guard.Unlock(req.Context, req.Unlock)
		if err != nil {
			return GateView{}, err
		}
		// Arming production is an event, not a setting, so it lands on the
		// timeline beside every other change.
		s.live.Record(auditKey(req.Context), timeline.Entry{
			At:     rec.At,
			Kind:   timeline.KindBreakGlass,
			Title:  "unlocked " + env.Name + " for writes",
			Detail: rec.Reason,
			Source: "session",
			Actor:  timeline.Actor{Kind: timeline.ActorKubectl, Name: "this kubeside session"},
		})
	}

	// An unlock on its own asks about the environment rather than about a
	// specific object: somebody arming prod before deciding what to do in it.
	if req.Verb == "" {
		action := guard.Action{
			Verb: "write", Resource: "environment", Name: env.Name, Namespace: req.Namespace,
		}
		return GateView{
			Gate:       s.guard.Check(req.Context, env, action),
			Permission: rbac.Permission{Allowed: true},
		}, nil
	}

	action := guard.Action{
		Verb: req.Verb, Resource: req.Resource, Name: req.Name, Namespace: req.Namespace,
		Kubectl: kubectlFor(req),
		Blast:   s.blastRadius(req),
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	// Connect first: an unconnected cluster cannot answer, and "could not
	// check" for a context nobody has opened yet is noise rather than a
	// finding.
	if err := s.mgr.Connect(ctx, req.Context); err != nil {
		return GateView{
			Gate: s.guard.Check(req.Context, env, action),
			Permission: rbac.Permission{
				Reason: fmt.Sprintf("could not reach %s to check permission: %v", req.Context, err),
			},
		}, nil
	}
	resource := pluralize(req.Resource)
	perm := s.perms.Can(ctx, req.Context, rbac.Action{
		Verb: req.Verb, Resource: resource, Group: groupOf(resource),
		Name: req.Name, Namespace: req.Namespace,
	})

	return GateView{Gate: s.guard.Check(req.Context, env, action), Permission: perm}, nil
}

// confirmed enforces the ceremony the gate asked for.
//
// The guard names what has to be typed; this compares it. A confirmation the
// server emits and never reads is a dialog rather than a control, and the
// distinction matters most for whoever automates around the UI, which is the
// case the ceremony exists for.
func confirmed(gate guard.Gate, typed string) error {
	if gate.Require != guard.RequireTypedName {
		return nil
	}
	if typed == gate.Confirm {
		return nil
	}
	return &ForbiddenError{Reason: fmt.Sprintf(
		"%s asks you to confirm this by typing %q; %s is not a place to act by accident",
		gate.Environment, gate.Confirm, gate.Environment)}
}

// blastRadius counts what an action touches, from the snapshot already in hand.
//
// A radius nobody computed renders as unknown rather than as zero: "0 pods
// affected" is a claim, and an uncomputed number is not one.
func (s *Service) blastRadius(req GateRequest) guard.Blast {
	snap, ok := s.lastSnapshot(req.Context)
	if !ok {
		return guard.Blast{Unknown: true}
	}

	switch req.Resource {
	case "pod":
		for _, a := range snap.Apps {
			for _, w := range a.Workloads {
				if w.Kind == "Pod" && w.Name == req.Name && w.Namespace == req.Namespace {
					return guard.Blast{
						Pods:    1,
						Summary: fmt.Sprintf("one of %d pods backing %s", countPods(a), a.Key.Name),
					}
				}
			}
		}
	default:
		for _, a := range snap.Apps {
			if a.Key.Name == req.Name && a.Key.Namespace == req.Namespace {
				n := countPods(a)
				return guard.Blast{Pods: n, Summary: fmt.Sprintf("%d pods backing %s", n, a.Key.Name)}
			}
		}
	}
	return guard.Blast{Unknown: true}
}

func countPods(a apps.App) int {
	n := 0
	for _, w := range a.Workloads {
		if w.Kind == "Pod" {
			n++
		}
	}
	return n
}

// kubectlFor renders the equivalent command, which is how somebody checks the
// tool's understanding against their own before agreeing to it.
//
// Two verbs do not fit the generic shape. "kubectl exec pod checkout" is not a
// command, and neither is a port-forward without a port, so each is rendered
// properly or not at all. A line that would error teaches the opposite of what
// this line is for.
func kubectlFor(req GateRequest) string {
	where := fmt.Sprintf("-n %s --context %s", req.Namespace, req.Context)

	switch req.Verb {
	case "exec":
		if req.Pod == "" {
			return ""
		}
		container := ""
		if req.Container != "" {
			container = fmt.Sprintf(" -c %s", req.Container)
		}
		return fmt.Sprintf("kubectl exec -it %s%s %s -- sh", req.Pod, container, where)

	case "port-forward":
		if req.Pod == "" || req.RemotePort == 0 {
			return ""
		}
		return fmt.Sprintf("kubectl port-forward %s %d %s", req.Pod, req.RemotePort, where)
	}

	return fmt.Sprintf("kubectl %s %s %s %s", req.Verb, req.Resource, req.Name, where)
}

// groupOf names the API group a resource lives in. A resource name is only
// unique within its group, and asking about a core-group "deployments" denies
// everybody, cluster-admin included, because no such resource exists.
func groupOf(resource string) string {
	switch resource {
	case "deployments", "statefulsets", "daemonsets", "replicasets":
		return "apps"
	case "jobs":
		return "batch"
	case "cronjobs":
		return "batch"
	}
	return ""
}

// pluralize turns the resource a dialog names into the one an access review
// wants. Only the kinds kubeside acts on are listed: guessing at plurals for
// arbitrary resources would produce permission questions nobody can answer.
func pluralize(resource string) string {
	switch resource {
	case "pod":
		return "pods"
	case "deployment":
		return "deployments"
	case "statefulset":
		return "statefulsets"
	case "daemonset":
		return "daemonsets"
	}
	return resource
}

// ExecRequest names one container to open a shell in.
type ExecRequest struct {
	Context   string
	Namespace string
	Pod       string
	Container string
	Workload  string
	// Confirm is what the developer typed. The guard says what it has to
	// match, and the server compares it: a confirmation the server does not
	// check is a dialog, not a control.
	Confirm string
}

// StartExec opens an interactive session, after both gates agree.
//
// The order matters and is not an accident. The cluster is asked first, because
// RBAC is the boundary; the write policy is checked second, because it is
// ergonomics on top. A session that opens leaves a record on the timeline: a
// shell in production is an event.
func (s *Service) StartExec(ctx context.Context, req ExecRequest, onOutput func([]byte)) (*exec.Session, <-chan error, error) {
	kctx, ok := s.cfg.Get(req.Context)
	if !ok {
		return nil, nil, errUnknownContext(req.Context)
	}
	if req.Pod == "" || req.Namespace == "" {
		return nil, nil, fmt.Errorf("exec needs a namespace and a pod")
	}

	connectCtx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.mgr.Connect(connectCtx, req.Context); err != nil {
		return nil, nil, err
	}

	// The security boundary first.
	perm := s.perms.Can(connectCtx, req.Context, rbac.Action{
		Verb: "create", Resource: "pods", Subresource: "exec", Namespace: req.Namespace,
	})
	if !perm.Allowed {
		return nil, nil, &ForbiddenError{Reason: perm.Reason}
	}

	// The pod is the scope, not a label. A namespace-scoped permission covers
	// every team sharing that namespace, so without this a caller reaches pods
	// they never opened and the record lands under whatever workload they
	// claimed.
	app, err := s.appOf(connectCtx, req.Context, req.Namespace, req.Workload)
	if err != nil {
		return nil, nil, err
	}
	if !hasPod(app, req.Pod) {
		return nil, nil, fmt.Errorf(
			"pod %s is not part of %s in %s; a shell opens in one workload's pods, not any pod in the namespace",
			req.Pod, req.Workload, req.Namespace)
	}

	// Then the guardrail, which is about acting in the wrong window rather
	// than about authority.
	env := s.conf.Environment(kctx)
	gate := s.guard.Check(req.Context, env, guard.Action{
		// What has to be typed is the workload, not the pod. A pod name is a
		// hash the developer never chose and cannot be expected to reproduce,
		// and a ceremony nobody can complete is one everybody routes around.
		Verb: "exec", Resource: "pod", Name: app.Key.Name, Namespace: req.Namespace,
		Kubectl: fmt.Sprintf("kubectl exec -it %s -c %s -n %s --context %s -- sh",
			req.Pod, req.Container, req.Namespace, req.Context),
		Blast: guard.Blast{Pods: 1, Summary: "one container of " + req.Pod},
	})
	if !gate.Permitted {
		return nil, nil, &ForbiddenError{Reason: gate.Reason}
	}
	if err := confirmed(gate, req.Confirm); err != nil {
		return nil, nil, err
	}

	session := exec.New(exec.KubeExecutor{
		RESTConfig: func(contextName string) (*rest.Config, error) {
			return kubeconfig.RESTConfigFor(s.opts, contextName)
		},
	})
	done := session.Start(ctx, exec.Target{
		Context: req.Context, Namespace: req.Namespace, Pod: req.Pod, Container: req.Container,
	}, onOutput)

	// Filed under the workload the pod actually belongs to, resolved above,
	// rather than the one the caller named.
	s.live.Record(sessionKey(req.Context, req.Namespace, app.Key.Name), timeline.Entry{
		At:     time.Now(),
		Kind:   timeline.KindExec,
		Title:  "opened a shell in " + req.Pod,
		Detail: "container " + req.Container + " in " + env.Name,
		Source: "session",
		Actor:  timeline.Actor{Kind: timeline.ActorKubectl, Name: "this kubeside session"},
	})

	return session, done, nil
}

package api

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/dynaum/kubeside/internal/apps"
	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/logs"
	"github.com/dynaum/kubeside/internal/metrics"
	"github.com/dynaum/kubeside/internal/resolved"
	"github.com/dynaum/kubeside/internal/session"
	"github.com/dynaum/kubeside/internal/timeline"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
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
	st, _ := s.mgr.Status(name)

	if !st.State.HasData() {
		view := AppsView{Context: name, State: st.State.String(), Apps: []AppView{}}
		if connErr != nil {
			view.Error = connErr.Error()
		}
		return view, nil
	}

	client, ok := s.mgr.ClientFor(name)
	if !ok {
		return AppsView{Context: name, State: st.State.String(), Apps: []AppView{}}, nil
	}

	snap, err := clusters.Fetch(ctx, client, s.cfg.MustGet(name))
	if err != nil {
		return AppsView{Context: name, State: st.State.String(), Error: err.Error(), Apps: []AppView{}}, nil
	}

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
	snap, err := clusters.Fetch(ctx, client, s.cfg.MustGet(contextName))
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
		return s.mayGetSecret(ctx, client, namespace, secret)
	})
	return ConfigView{
		Context:    contextName,
		Namespace:  namespace,
		Workload:   workload,
		Pod:        cfg.Pod,
		Containers: cfg.Containers,
		Caveat:     cfg.Caveat,
	}, nil
}

// representativePod picks the pod to read configuration from: a ready one when
// there is one, because a crash-looping pod may never have resolved its
// environment at all.
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

// mayGetSecret asks the cluster whether this reader may get one Secret.
//
// The question is asked of the apiserver rather than guessed from a role, and
// it names the specific Secret: a reader with get on one Secret and not another
// should see exactly one reveal enabled.
func (s *Service) mayGetSecret(ctx context.Context, client kubernetes.Interface, namespace, secret string) (bool, string) {
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace, Verb: "get", Resource: "secrets", Name: secret,
			},
		},
	}
	got, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		// An unanswerable question is not a yes.
		return false, fmt.Sprintf("could not check permission to get secret %s: %v", secret, err)
	}
	if !got.Status.Allowed {
		return false, fmt.Sprintf("needs get on secret %s in %s", secret, namespace)
	}
	return true, ""
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

	if allowed, reason := s.mayGetSecret(ctx, client, namespace, secret); !allowed {
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
	if workload == "" {
		return
	}
	s.live.Record(sessionKey(contextName, namespace, workload), timeline.Entry{
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

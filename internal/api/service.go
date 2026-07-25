package api

import (
	"context"
	"fmt"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/config"
	"github.com/dynaum/kubeside/internal/kubeconfig"
	"github.com/dynaum/kubeside/internal/logs"
	"github.com/dynaum/kubeside/internal/metrics"
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
	return &Service{cfg: cfg, mgr: mgr, conf: conf, opts: opts, timeout: timeout}
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
	client, ok := s.mgr.ClientFor(contextName)
	if !ok {
		return nil, fmt.Errorf("context %q is not connected", contextName)
	}
	snap, err := clusters.Fetch(ctx, client, s.cfg.MustGet(contextName))
	if err != nil {
		return nil, err
	}
	for _, a := range snap.Apps {
		if a.Key.Namespace != namespace || a.Key.Name != workload {
			continue
		}
		var pods []string
		for _, w := range a.Workloads {
			if w.Kind == "Pod" {
				pods = append(pods, w.Name)
			}
		}
		return pods, nil
	}
	return nil, fmt.Errorf("no workload %q in namespace %q of context %q", workload, namespace, contextName)
}

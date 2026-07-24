package api

import (
	"context"
	"fmt"
	"time"

	"github.com/dynaum/kubeside/internal/clusters"
	"github.com/dynaum/kubeside/internal/kubeconfig"
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
	opts    kubeconfig.Options
	timeout time.Duration
}

// NewService builds the API backend.
func NewService(cfg *kubeconfig.Config, mgr *clusters.Manager, opts kubeconfig.Options, timeout time.Duration) *Service {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &Service{cfg: cfg, mgr: mgr, opts: opts, timeout: timeout}
}

// Contexts returns every context with its live connection state, in connect
// order so the current context leads.
func (s *Service) Contexts() []ContextView {
	statuses := s.mgr.Statuses()
	out := make([]ContextView, 0, len(statuses))
	for _, st := range statuses {
		kctx, _ := s.cfg.Get(st.Context)
		v := ContextView{
			Name:    st.Context,
			Current: kctx.IsCurrent,
			State:   st.State.String(),
			HasData: st.State.HasData(),
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
func (s *Service) metricsInfo(ctx context.Context, name string) MetricsInfo {
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

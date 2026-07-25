package forward

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// KubeTunnel is the production tunnel: SPDY to the apiserver, exactly the
// mechanism kubectl port-forward uses.
type KubeTunnel struct {
	// RESTConfig returns the config for one context. Credential plugins run
	// here, the same as everywhere else.
	RESTConfig func(contextName string) (*rest.Config, error)
}

// Open holds the forward until ctx is cancelled.
func (k KubeTunnel) Open(ctx context.Context, t Target, ready func(localPort int)) error {
	cfg, err := k.RESTConfig(t.Context)
	if err != nil {
		return err
	}

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return err
	}

	host, err := url.Parse(cfg.Host)
	if err != nil {
		return fmt.Errorf("parse apiserver address: %w", err)
	}
	target := &url.URL{
		Scheme: host.Scheme,
		Host:   host.Host,
		Path: fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s/portforward",
			host.Path, t.Namespace, t.Pod),
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, target)

	stop := make(chan struct{}, 1)
	up := make(chan struct{}, 1)
	// A local port of zero asks the operating system for a free one, which is
	// what keeps a second forward from colliding with the first.
	ports := []string{fmt.Sprintf("%d:%d", t.LocalPort, t.RemotePort)}

	pf, err := portforward.NewOnAddresses(dialer, []string{LocalAddress}, ports, stop, up, io.Discard, io.Discard)
	if err != nil {
		return err
	}

	// One closer, so a cancelled context and a finished forward cannot both
	// close the same channel.
	go func() {
		<-ctx.Done()
		close(stop)
	}()

	go func() {
		<-up
		got, err := pf.GetPorts()
		if err != nil || len(got) == 0 {
			return
		}
		ready(int(got[0].Local))
	}()

	return pf.ForwardPorts()
}

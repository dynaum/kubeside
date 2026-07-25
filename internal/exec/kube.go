package exec

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// KubeExecutor runs the session against a cluster, the same mechanism kubectl
// exec uses. Credentials stay in this process: the browser never holds one.
type KubeExecutor struct {
	// RESTConfig returns the config for one context, which is where credential
	// plugins run.
	RESTConfig func(contextName string) (*rest.Config, error)
}

// Run streams until the shell exits or the context is cancelled.
func (k KubeExecutor) Run(ctx context.Context, t Target, streams Streams) error {
	cfg, err := k.RESTConfig(t.Context)
	if err != nil {
		return err
	}

	client, err := rest.RESTClientFor(withCoreDefaults(cfg))
	if err != nil {
		return err
	}

	req := client.Post().
		Resource("pods").
		Namespace(t.Namespace).
		Name(t.Pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: t.Container,
			Command:   t.Command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	// SPDY first with a websocket fallback is what kubectl does, and an
	// apiserver behind a proxy that mangles one usually passes the other.
	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return err
	}

	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             streams.Stdin,
		Stdout:            streams.Stdout,
		Stderr:            streams.Stderr,
		Tty:               true,
		TerminalSizeQueue: sizeQueue{ctx: ctx, in: streams.Resize},
	})
}

// withCoreDefaults fills the group, version, and codec a raw REST client needs.
func withCoreDefaults(cfg *rest.Config) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.GroupVersion = &corev1.SchemeGroupVersion
	out.APIPath = "/api"
	out.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	return out
}

// sizeQueue adapts the session's resize channel to what client-go wants. A nil
// return ends the queue, which is how a cancelled session stops being asked.
type sizeQueue struct {
	ctx context.Context
	in  <-chan Size
}

func (q sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case <-q.ctx.Done():
		return nil
	case s, ok := <-q.in:
		if !ok {
			return nil
		}
		return &remotecommand.TerminalSize{Width: s.Cols, Height: s.Rows}
	}
}

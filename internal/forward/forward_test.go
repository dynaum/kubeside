package forward

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTunnel stands in for a SPDY port-forward. It reports the local port it
// bound and stays up until its context is cancelled, which is the only
// behaviour the manager depends on.
type fakeTunnel struct {
	mu      sync.Mutex
	opened  []Target
	fail    error
	stalled bool
	stopped int
}

func (f *fakeTunnel) Open(ctx context.Context, t Target, ready func(local int)) error {
	f.mu.Lock()
	f.opened = append(f.opened, t)
	err := f.fail
	stalled := f.stalled
	f.mu.Unlock()

	if err != nil {
		return err
	}
	if stalled {
		<-ctx.Done()
		return ctx.Err()
	}

	// A real tunnel binds a listener; binding a real one here keeps the port
	// bookkeeping honest.
	l, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		return lerr
	}
	defer l.Close()
	ready(l.Addr().(*net.TCPAddr).Port)

	<-ctx.Done()
	f.mu.Lock()
	f.stopped++
	f.mu.Unlock()
	return nil
}

func (f *fakeTunnel) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened)
}

func (f *fakeTunnel) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func target() Target {
	return Target{
		Context: "qa1", Namespace: "team-a", Workload: "checkout",
		Pod: "checkout-1", RemotePort: 8080, Environment: "qa", Risk: "low",
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartReportsTheBoundLocalPort(t *testing.T) {
	m := New(&fakeTunnel{})
	t.Cleanup(m.StopAll)

	f, err := m.Start(context.Background(), target())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.LocalPort == 0 {
		t.Fatal("no local port reported; the developer cannot use what they cannot address")
	}
	if f.State != StateReady {
		t.Errorf("state = %q, want ready", f.State)
	}
}

// The environment travels with the forward. A tunnel into production is a
// different thing from one into qa, and the colour is how that reads at a
// glance.
func TestForwardCarriesItsEnvironment(t *testing.T) {
	m := New(&fakeTunnel{})
	t.Cleanup(m.StopAll)

	f, err := m.Start(context.Background(), target())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.Environment != "qa" || f.Risk != "low" {
		t.Fatalf("forward = %+v, want the environment carried through", f)
	}
}

func TestListReturnsEveryLiveForward(t *testing.T) {
	m := New(&fakeTunnel{})
	t.Cleanup(m.StopAll)

	second := target()
	second.RemotePort = 9090
	if _, err := m.Start(context.Background(), target()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if got := m.List(); len(got) != 2 {
		t.Fatalf("list = %d, want both forwards", len(got))
	}
}

func TestStopEndsTheTunnel(t *testing.T) {
	tun := &fakeTunnel{}
	m := New(tun)
	t.Cleanup(m.StopAll)

	f, err := m.Start(context.Background(), target())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(f.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	waitFor(t, "the tunnel to close", func() bool { return tun.stopCount() == 1 })
	if len(m.List()) != 0 {
		t.Fatalf("list = %+v, want the stopped forward gone", m.List())
	}
}

func TestStoppingAnUnknownForwardIsAnError(t *testing.T) {
	m := New(&fakeTunnel{})
	if err := m.Stop("nope"); err == nil {
		t.Fatal("want an error for an id that is not running")
	}
}

// Nothing survives the process. A tunnel into production left open by a crashed
// UI is exactly the kind of thing this tool must not leave behind.
func TestStopAllClosesEverything(t *testing.T) {
	tun := &fakeTunnel{}
	m := New(tun)

	for _, port := range []int{8080, 9090, 3000} {
		tg := target()
		tg.RemotePort = port
		if _, err := m.Start(context.Background(), tg); err != nil {
			t.Fatal(err)
		}
	}

	m.StopAll()
	waitFor(t, "every tunnel to close", func() bool { return tun.stopCount() == 3 })
	if len(m.List()) != 0 {
		t.Fatalf("list = %+v after StopAll", m.List())
	}
}

// Asking twice for the same thing should hand back the tunnel that already
// exists rather than opening a second one on a different port.
func TestStartingTheSameTargetTwiceReusesTheForward(t *testing.T) {
	tun := &fakeTunnel{}
	m := New(tun)
	t.Cleanup(m.StopAll)

	first, err := m.Start(context.Background(), target())
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Start(context.Background(), target())
	if err != nil {
		t.Fatal(err)
	}

	if first.ID != second.ID || first.LocalPort != second.LocalPort {
		t.Fatalf("first = %+v, second = %+v, want the same forward", first, second)
	}
	if tun.openCount() != 1 {
		t.Errorf("opened %d tunnels for one target", tun.openCount())
	}
}

// A forward that cannot be established says why, and leaves nothing behind
// claiming to be listening.
func TestAFailedForwardReportsTheReason(t *testing.T) {
	tun := &fakeTunnel{fail: errors.New("pod checkout-1 has no port 8080")}
	m := New(tun)
	t.Cleanup(m.StopAll)

	_, err := m.Start(context.Background(), target())
	if err == nil {
		t.Fatal("want the failure reported")
	}
	if !strings.Contains(err.Error(), "no port 8080") {
		t.Errorf("error = %q, should carry the reason", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("list = %+v, a failed forward must not be listed as live", m.List())
	}
}

// A tunnel that never comes up must not hang the request forever.
func TestStartTimesOutRatherThanHanging(t *testing.T) {
	m := New(&fakeTunnel{stalled: true})
	m.startTimeout = 50 * time.Millisecond
	t.Cleanup(m.StopAll)

	start := time.Now()
	_, err := m.Start(context.Background(), target())
	if err == nil {
		t.Fatal("want a timeout")
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %v; the caller should not wait on a tunnel that is not coming up", time.Since(start))
	}
	if len(m.List()) != 0 {
		t.Errorf("list = %+v after a timeout", m.List())
	}
}

// The listener is loopback only. Forwarding a production database onto the
// office network would be a hole this tool opened.
func TestLocalAddressIsLoopbackOnly(t *testing.T) {
	if LocalAddress != "127.0.0.1" {
		t.Fatalf("LocalAddress = %q; anything but loopback exposes the cluster to the network", LocalAddress)
	}
}

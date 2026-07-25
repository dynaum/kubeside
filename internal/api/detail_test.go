package api

import (
	"testing"
	"time"

	"github.com/dynaum/kubeside/internal/apps"
)

func pod(name string, st *apps.Status, ageDays int) apps.Object {
	return apps.Object{
		Kind: "Pod", Name: name, Namespace: "team-a", Status: st,
		Created: time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour),
	}
}

func TestPodsOfRendersEveryReplica(t *testing.T) {
	a := apps.App{
		Key: apps.Key{Namespace: "team-a", Name: "checkout"}, Kind: "Deployment",
		Workloads: []apps.Object{
			{Kind: "Deployment", Name: "checkout"},
			pod("checkout-1", &apps.Status{Phase: "Running", Ready: true}, 11),
			pod("checkout-2", &apps.Status{Phase: "Running", Ready: true, RestartCount: 2}, 2),
		},
	}

	got := PodsOf(a, time.Now())
	if len(got) != 2 {
		t.Fatalf("pods = %d, want one per replica and nothing else", len(got))
	}
	if got[0].AgeSec < 10*24*3600 {
		t.Errorf("age = %d, want about eleven days", got[0].AgeSec)
	}
	if got[1].Restarts != 2 {
		t.Errorf("restarts = %d", got[1].Restarts)
	}
}

// The replica that needs a human must not be somewhere in the middle of six.
func TestPodsOfPutsTheWorstFirst(t *testing.T) {
	a := apps.App{Workloads: []apps.Object{
		pod("checkout-ok", &apps.Status{Phase: "Running", Ready: true}, 1),
		pod("checkout-bad", &apps.Status{Phase: "Running", WaitingReason: "CrashLoopBackOff"}, 1),
		pod("checkout-pending", &apps.Status{Phase: "Pending"}, 1),
	}}

	got := PodsOf(a, time.Now())
	if got[0].Name != "checkout-bad" || got[0].Health != "failed" {
		t.Fatalf("first pod = %+v, want the failing one", got[0])
	}
	if got[len(got)-1].Name != "checkout-ok" {
		t.Errorf("last pod = %q, want the healthy one", got[len(got)-1].Name)
	}
}

func TestPodHealthMatchesTheAppListChannel(t *testing.T) {
	cases := []struct {
		name string
		st   *apps.Status
		want string
	}{
		{"ready", &apps.Status{Phase: "Running", Ready: true}, "healthy"},
		{"crashlooping", &apps.Status{Phase: "Running", WaitingReason: "CrashLoopBackOff"}, "failed"},
		{"oomkilled", &apps.Status{Phase: "Running", TerminatedReason: "OOMKilled"}, "failed"},
		{"pending", &apps.Status{Phase: "Pending"}, "progressing"},
		{"running not ready", &apps.Status{Phase: "Running"}, "degraded"},
		{"completed", &apps.Status{Phase: "Succeeded"}, "healthy"},
		{"unread", nil, "unknown"},
	}
	for _, c := range cases {
		if got := podHealth(c.st); got != c.want {
			t.Errorf("%s: health = %q, want %q", c.name, got, c.want)
		}
	}
}

// A pod read metadata-only has no status. Rendering it as healthy would be a
// guess in the dangerous direction.
func TestPodWithoutStatusIsUnknownNotHealthy(t *testing.T) {
	got := PodsOf(apps.App{Workloads: []apps.Object{pod("checkout-1", nil, 1)}}, time.Now())
	if got[0].Health != "unknown" {
		t.Fatalf("health = %q, want unknown", got[0].Health)
	}
}

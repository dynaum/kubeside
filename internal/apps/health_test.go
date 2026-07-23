package apps

import (
	"strings"
	"testing"
	"time"
)

func dep(ns, name string, s *Status) Object {
	o := obj("Deployment", ns, name)
	o.Status = s
	return o
}

func pod(ns, name string, s *Status) Object {
	o := obj("Pod", ns, name)
	o.Status = s
	return o
}

func app(kind string, workloads ...Object) App {
	return App{Key: Key{"ns", "app"}, Kind: kind, Workloads: workloads}
}

func TestHealthyWhenReadyEqualsDesired(t *testing.T) {
	got := Assess(app("Deployment",
		dep("ns", "app", &Status{DesiredReplicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3}),
		pod("ns", "app-a", &Status{Phase: "Running", Ready: true}),
	))
	if got.Health != HealthHealthy {
		t.Fatalf("health = %s (%s), want healthy", got.Health, got.Detail)
	}
	if !strings.Contains(got.Detail, "3 of 3") {
		t.Errorf("detail = %q, want the replica counts", got.Detail)
	}
}

// A crash loop must never be masked by healthy siblings.
func TestCrashLoopBeatsHealthySiblings(t *testing.T) {
	got := Assess(app("Deployment",
		dep("ns", "app", &Status{DesiredReplicas: 6, ReadyReplicas: 5, UpdatedReplicas: 6}),
		pod("ns", "app-a", &Status{Phase: "Running", Ready: true}),
		pod("ns", "app-b", &Status{Phase: "Running", Ready: true}),
		pod("ns", "app-c", &Status{Phase: "Pending", WaitingReason: "CrashLoopBackOff"}),
	))
	if got.Health != HealthFailed {
		t.Fatalf("health = %s, want failed", got.Health)
	}
	if got.Reason != "CrashLoopBackOff" {
		t.Errorf("reason = %q, want CrashLoopBackOff", got.Reason)
	}
	if !strings.Contains(got.Detail, "app-c") {
		t.Errorf("detail = %q, should name the offending pod", got.Detail)
	}
}

func TestImagePullFailuresAreFailed(t *testing.T) {
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName", "CreateContainerConfigError"} {
		got := Assess(app("Deployment",
			dep("ns", "app", &Status{DesiredReplicas: 1, ReadyReplicas: 1}),
			pod("ns", "p", &Status{WaitingReason: reason}),
		))
		if got.Health != HealthFailed {
			t.Errorf("%s = %s, want failed", reason, got.Health)
		}
	}
}

func TestOOMKilledIsFailed(t *testing.T) {
	got := Assess(app("Deployment",
		dep("ns", "app", &Status{DesiredReplicas: 1, ReadyReplicas: 1}),
		pod("ns", "p", &Status{TerminatedReason: "OOMKilled"}),
	))
	if got.Health != HealthFailed || got.Reason != "OOMKilled" {
		t.Fatalf("got %s/%s, want failed/OOMKilled", got.Health, got.Reason)
	}
}

func TestProgressDeadlineExceededIsFailed(t *testing.T) {
	got := Assess(app("Deployment", dep("ns", "app", &Status{
		DesiredReplicas: 3, ReadyReplicas: 2,
		Conditions: []Condition{{
			Type: "Progressing", Status: "False",
			Reason:  "ProgressDeadlineExceeded",
			Message: "ReplicaSet app-7d9f has timed out progressing",
		}},
	})))
	if got.Health != HealthFailed {
		t.Fatalf("health = %s, want failed", got.Health)
	}
	if got.Reason != "ProgressDeadlineExceeded" {
		t.Errorf("reason = %q", got.Reason)
	}
	if !strings.Contains(got.Detail, "timed out") {
		t.Errorf("detail = %q, want the controller message", got.Detail)
	}
}

func TestNoReplicasReadyIsFailedNotDegraded(t *testing.T) {
	got := Assess(app("Deployment", dep("ns", "app", &Status{DesiredReplicas: 3, ReadyReplicas: 0})))
	if got.Health != HealthFailed {
		t.Fatalf("health = %s, want failed: nothing is serving", got.Health)
	}
}

// A rollout is below desired by design. Flagging that as degraded would mark
// every deploy as a problem.
func TestRolloutInFlightIsProgressingNotDegraded(t *testing.T) {
	got := Assess(app("Deployment", dep("ns", "app", &Status{
		DesiredReplicas: 6, ReadyReplicas: 4, UpdatedReplicas: 2,
		Conditions: []Condition{{Type: "Progressing", Status: "True", Reason: "ReplicaSetUpdated"}},
	})))
	if got.Health != HealthProgressing {
		t.Fatalf("health = %s (%s), want progressing", got.Health, got.Detail)
	}
}

func TestCompletedRolloutIsNotProgressing(t *testing.T) {
	got := Assess(app("Deployment", dep("ns", "app", &Status{
		DesiredReplicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3,
		Conditions: []Condition{{Type: "Progressing", Status: "True", Reason: "NewReplicaSetAvailable"}},
	})))
	if got.Health != HealthHealthy {
		t.Fatalf("health = %s, want healthy: NewReplicaSetAvailable means the rollout finished", got.Health)
	}
}

func TestUnobservedGenerationIsProgressing(t *testing.T) {
	got := Assess(app("Deployment", dep("ns", "app", &Status{
		DesiredReplicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3,
		Generation: 9, ObservedGeneration: 8,
	})))
	if got.Health != HealthProgressing || got.Reason != "GenerationLag" {
		t.Fatalf("got %s/%s, want progressing/GenerationLag", got.Health, got.Reason)
	}
}

func TestBelowDesiredOutsideRolloutIsDegraded(t *testing.T) {
	got := Assess(app("Deployment", dep("ns", "app", &Status{
		DesiredReplicas: 6, ReadyReplicas: 5, UpdatedReplicas: 6,
	})))
	if got.Health != HealthDegraded {
		t.Fatalf("health = %s, want degraded", got.Health)
	}
	if !strings.Contains(got.Detail, "5 of 6") {
		t.Errorf("detail = %q, want the counts", got.Detail)
	}
}

func TestRepeatedRestartsAreDegraded(t *testing.T) {
	got := Assess(app("Deployment",
		dep("ns", "app", &Status{DesiredReplicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2}),
		pod("ns", "app-a", &Status{Phase: "Running", Ready: true, RestartCount: 14}),
	))
	if got.Health != HealthDegraded || got.Reason != "Restarting" {
		t.Fatalf("got %s/%s, want degraded/Restarting", got.Health, got.Reason)
	}
	if !strings.Contains(got.Detail, "14") {
		t.Errorf("detail = %q, want the restart count", got.Detail)
	}
}

func TestOneRestartIsNotDegraded(t *testing.T) {
	got := Assess(app("Deployment",
		dep("ns", "app", &Status{DesiredReplicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2}),
		pod("ns", "app-a", &Status{Phase: "Running", Ready: true, RestartCount: 1}),
	))
	if got.Health != HealthHealthy {
		t.Fatalf("health = %s, want healthy: a single restart is noise, not a signal", got.Health)
	}
}

func TestRunningButNotReadyNamesTheProbe(t *testing.T) {
	got := Assess(app("Deployment",
		dep("ns", "app", &Status{DesiredReplicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2}),
		pod("ns", "app-a", &Status{
			Phase: "Running", Ready: false,
			ProbeFailure: "readiness probe failed: dial tcp 127.0.0.1:8080 i/o timeout",
		}),
	))
	if got.Health != HealthDegraded {
		t.Fatalf("health = %s, want degraded", got.Health)
	}
	if !strings.Contains(got.Detail, "readiness probe") {
		t.Errorf("detail = %q, must name the probe rather than restate the state", got.Detail)
	}
}

// Unknown must never be rendered as healthy.
func TestNoStatusIsUnknownNotHealthy(t *testing.T) {
	got := Assess(app("Deployment", obj("Deployment", "ns", "app")))
	if got.Health != HealthUnknown {
		t.Fatalf("health = %s, want unknown: absence of status is not evidence of health", got.Health)
	}
	if got.Health == HealthHealthy {
		t.Fatal("unreadable status must never render as healthy")
	}
}

func TestCronJobSemantics(t *testing.T) {
	past := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		status *Status
		want   Health
		reason string
	}{
		{"last run succeeded", &Status{LastScheduleTime: &past, LastSuccessTime: &past}, HealthHealthy, "LastRunSucceeded"},
		{"last run failed", &Status{LastScheduleTime: &past, LastJobFailed: true}, HealthFailed, "LastRunFailed"},
		{"suspended", &Status{Suspended: true, LastSuccessTime: &past}, HealthDegraded, "Suspended"},
		{"in flight", &Status{ActiveJobs: 1, LastSuccessTime: &past}, HealthProgressing, "Running"},
		{"fired but never succeeded", &Status{LastScheduleTime: &past}, HealthDegraded, "NeverSucceeded"},
		{"never fired", &Status{}, HealthUnknown, "NeverRun"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := obj("CronJob", "ns", "cj")
			o.Status = tc.status
			got := Assess(App{Key: Key{"ns", "cj"}, Kind: "CronJob", Workloads: []Object{o}})
			if got.Health != tc.want || got.Reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s (%s)", got.Health, got.Reason, tc.want, tc.reason, got.Detail)
			}
		})
	}
}

// A CronJob has no replicas, so replica rules must not fabricate a verdict.
func TestCronJobDoesNotUseReplicaRules(t *testing.T) {
	o := obj("CronJob", "ns", "cj")
	past := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)
	o.Status = &Status{DesiredReplicas: 0, ReadyReplicas: 0, LastSuccessTime: &past, LastScheduleTime: &past}
	got := Assess(App{Key: Key{"ns", "cj"}, Kind: "CronJob", Workloads: []Object{o}})
	if got.Health != HealthHealthy {
		t.Fatalf("health = %s, want healthy: 0/0 replicas is meaningless for a schedule", got.Health)
	}
}

func TestAssessIsDeterministicAcrossPodOrder(t *testing.T) {
	bad := pod("ns", "app-z", &Status{WaitingReason: "CrashLoopBackOff"})
	alsoBad := pod("ns", "app-a", &Status{WaitingReason: "ImagePullBackOff"})
	d := dep("ns", "app", &Status{DesiredReplicas: 2, ReadyReplicas: 1})

	first := Assess(app("Deployment", d, bad, alsoBad))
	for i := 0; i < 10; i++ {
		if got := Assess(app("Deployment", d, alsoBad, bad)); got != first {
			t.Fatalf("verdict depends on pod order: %+v vs %+v", got, first)
		}
	}
	// Sorted by name, app-a wins.
	if first.Reason != "ImagePullBackOff" {
		t.Errorf("reason = %q, want the alphabetically first offender for determinism", first.Reason)
	}
}

func TestAttentionOrdersWorstFirst(t *testing.T) {
	order := []Health{HealthFailed, HealthDegraded, HealthProgressing, HealthUnknown, HealthHealthy}
	for i := 1; i < len(order); i++ {
		if order[i-1].Attention() >= order[i].Attention() {
			t.Fatalf("%s should sort before %s", order[i-1], order[i])
		}
	}
}

func TestHealthStringsAreStable(t *testing.T) {
	for h, want := range map[Health]string{
		HealthHealthy:     "healthy",
		HealthProgressing: "progressing",
		HealthDegraded:    "degraded",
		HealthFailed:      "failed",
		HealthUnknown:     "unknown",
	} {
		if got := h.String(); got != want {
			t.Errorf("Health(%d) = %q, want %q", h, got, want)
		}
	}
}

func TestEveryAssessmentExplainsItself(t *testing.T) {
	cases := []App{
		app("Deployment", dep("ns", "a", &Status{DesiredReplicas: 1, ReadyReplicas: 1})),
		app("Deployment", dep("ns", "a", &Status{DesiredReplicas: 3, ReadyReplicas: 0})),
		app("Deployment", dep("ns", "a", &Status{DesiredReplicas: 3, ReadyReplicas: 2, UpdatedReplicas: 3})),
		app("Deployment", obj("Deployment", "ns", "a")),
	}
	for _, a := range cases {
		got := Assess(a)
		if got.Reason == "" || got.Detail == "" {
			t.Errorf("assessment %+v lacks an explanation; a badge with no why is not actionable", got)
		}
	}
}

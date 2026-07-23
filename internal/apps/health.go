package apps

import (
	"fmt"
	"sort"
	"time"
)

// Health is an app's derived state.
//
// The derivation is specified in docs/03-product-spec.md rather than left to
// whatever the first implementation happened to compute, because the app list
// sorts on this and the promotion matrix colours cells by it.
type Health int

const (
	// HealthUnknown: not enough was readable to say. Never rendered as healthy.
	HealthUnknown Health = iota
	// HealthHealthy: ready equals desired, rollout complete, no recent restarts.
	HealthHealthy
	// HealthProgressing: a rollout is in flight inside its deadline.
	HealthProgressing
	// HealthDegraded: below desired outside a rollout, or restarting.
	HealthDegraded
	// HealthFailed: crash looping, unpullable image, or deadline exceeded.
	HealthFailed
)

func (h Health) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthProgressing:
		return "progressing"
	case HealthDegraded:
		return "degraded"
	case HealthFailed:
		return "failed"
	case HealthUnknown:
		return "unknown"
	}
	return "unknown"
}

// Attention orders apps for the list: worst first, so the two things that need
// a human float above the forty that do not.
func (h Health) Attention() int {
	switch h {
	case HealthFailed:
		return 0
	case HealthDegraded:
		return 1
	case HealthProgressing:
		return 2
	case HealthUnknown:
		return 3
	case HealthHealthy:
		return 4
	}
	return 5
}

// Assessment is a health verdict plus why it was reached.
//
// Reason and Detail exist because "degraded" on its own is not actionable. The
// UI shows Detail when the badge is clicked, and it must name the condition or
// probe rather than restating the state.
type Assessment struct {
	Health Health
	Reason string // short machine-ish token: CrashLoopBackOff, BelowDesired
	Detail string // one sentence naming the condition, probe, or count
}

// restartWindow is how many restarts on a ready pod still counts as settled.
// One restart happens; a handful in the observation window does not.
const restartThreshold = 3

// Assess derives an app's health from its workloads.
//
// Evaluation order is first-match-wins, worst first, so a single crash-looping
// pod is never masked by five healthy siblings.
func Assess(a App) Assessment {
	if a.Kind == "CronJob" {
		return assessCronJob(a)
	}

	var (
		primary   *Object
		pods      []*Object
		anyStatus bool
	)
	for i := range a.Workloads {
		o := &a.Workloads[i]
		if o.Status != nil {
			anyStatus = true
		}
		switch {
		case o.Kind == "Pod":
			pods = append(pods, o)
		case topLevelKinds[o.Kind]:
			if primary == nil || rankOf(o.Kind) < rankOf(primary.Kind) {
				primary = o
			}
		}
	}

	if !anyStatus {
		return Assessment{
			Health: HealthUnknown,
			Reason: "NoStatus",
			Detail: "status was not readable for this app",
		}
	}

	// Failed beats everything: a crash loop is the loudest fact available.
	if v, ok := failedPod(pods); ok {
		return v
	}
	if primary != nil {
		if v, ok := failedWorkload(primary); ok {
			return v
		}
	}

	// Progressing before degraded: a rollout mid-flight is below desired by
	// design, and calling that degraded would flag every deploy.
	if primary != nil {
		if v, ok := progressing(primary); ok {
			return v
		}
	}

	if primary != nil {
		if v, ok := degradedWorkload(primary); ok {
			return v
		}
	}
	if v, ok := degradedPods(pods); ok {
		return v
	}

	if primary == nil && len(pods) == 0 {
		return Assessment{
			Health: HealthUnknown,
			Reason: "NoWorkload",
			Detail: "no workload or pod carried a readable status",
		}
	}

	return Assessment{
		Health: HealthHealthy,
		Reason: "Ready",
		Detail: readyDetail(primary),
	}
}

func failedPod(pods []*Object) (Assessment, bool) {
	// Sort so the verdict is deterministic when several pods are broken.
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for _, p := range pods {
		if p.Status == nil {
			continue
		}
		switch p.Status.WaitingReason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "InvalidImageName":
			return Assessment{
				Health: HealthFailed,
				Reason: p.Status.WaitingReason,
				Detail: fmt.Sprintf("pod %s is in %s", p.Name, p.Status.WaitingReason),
			}, true
		}
		if p.Status.TerminatedReason == "OOMKilled" {
			return Assessment{
				Health: HealthFailed,
				Reason: "OOMKilled",
				Detail: fmt.Sprintf("pod %s was OOMKilled", p.Name),
			}, true
		}
	}
	return Assessment{}, false
}

func failedWorkload(o *Object) (Assessment, bool) {
	if c, ok := o.Status.Find("Progressing"); ok && c.Status == "False" {
		reason := c.Reason
		if reason == "" {
			reason = "ProgressingFalse"
		}
		detail := fmt.Sprintf("rollout stalled: %s", reason)
		if c.Message != "" {
			detail = fmt.Sprintf("rollout stalled: %s", c.Message)
		}
		return Assessment{Health: HealthFailed, Reason: reason, Detail: detail}, true
	}
	if c, ok := o.Status.Find("ReplicaFailure"); ok && c.Status == "True" {
		return Assessment{
			Health: HealthFailed,
			Reason: "ReplicaFailure",
			Detail: firstNonEmpty(c.Message, "the controller could not create replicas"),
		}, true
	}
	// Desired replicas with none ready is a failure, not a degradation.
	if o.Status.DesiredReplicas > 0 && o.Status.ReadyReplicas == 0 {
		return Assessment{
			Health: HealthFailed,
			Reason: "NoneReady",
			Detail: fmt.Sprintf("0 of %d replicas ready", o.Status.DesiredReplicas),
		}, true
	}
	return Assessment{}, false
}

func progressing(o *Object) (Assessment, bool) {
	s := o.Status
	// The controller has not yet observed the latest spec.
	if s.Generation > 0 && s.ObservedGeneration > 0 && s.ObservedGeneration < s.Generation {
		return Assessment{
			Health: HealthProgressing,
			Reason: "GenerationLag",
			Detail: "the controller has not yet observed the latest change",
		}, true
	}
	if c, ok := s.Find("Progressing"); ok && c.Status == "True" && c.Reason != "NewReplicaSetAvailable" {
		return Assessment{
			Health: HealthProgressing,
			Reason: firstNonEmpty(c.Reason, "Progressing"),
			Detail: firstNonEmpty(c.Message, "a rollout is in flight"),
		}, true
	}
	// Updated below desired means new pods are still rolling out.
	if s.UpdatedReplicas > 0 && s.DesiredReplicas > 0 && s.UpdatedReplicas < s.DesiredReplicas {
		return Assessment{
			Health: HealthProgressing,
			Reason: "RollingOut",
			Detail: fmt.Sprintf("%d of %d replicas updated", s.UpdatedReplicas, s.DesiredReplicas),
		}, true
	}
	return Assessment{}, false
}

func degradedWorkload(o *Object) (Assessment, bool) {
	s := o.Status
	if s.DesiredReplicas > 0 && s.ReadyReplicas < s.DesiredReplicas {
		return Assessment{
			Health: HealthDegraded,
			Reason: "BelowDesired",
			Detail: fmt.Sprintf("%d of %d replicas ready", s.ReadyReplicas, s.DesiredReplicas),
		}, true
	}
	if c, ok := s.Find("Available"); ok && c.Status == "False" {
		return Assessment{
			Health: HealthDegraded,
			Reason: firstNonEmpty(c.Reason, "Unavailable"),
			Detail: firstNonEmpty(c.Message, "the workload reports itself unavailable"),
		}, true
	}
	return Assessment{}, false
}

func degradedPods(pods []*Object) (Assessment, bool) {
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	for _, p := range pods {
		if p.Status == nil {
			continue
		}
		if p.Status.RestartCount >= restartThreshold {
			return Assessment{
				Health: HealthDegraded,
				Reason: "Restarting",
				Detail: fmt.Sprintf("pod %s has restarted %d times", p.Name, p.Status.RestartCount),
			}, true
		}
		// A running pod that is not ready is failing a probe.
		if p.Status.Phase == "Running" && !p.Status.Ready {
			detail := fmt.Sprintf("pod %s is running but not ready", p.Name)
			if p.Status.ProbeFailure != "" {
				detail = fmt.Sprintf("pod %s: %s", p.Name, p.Status.ProbeFailure)
			}
			return Assessment{Health: HealthDegraded, Reason: "ProbeFailing", Detail: detail}, true
		}
	}
	return Assessment{}, false
}

// assessCronJob applies schedule semantics.
//
// Ready-over-desired means nothing for a CronJob, so reusing the replica rules
// would fabricate a verdict from fields that do not apply.
func assessCronJob(a App) Assessment {
	var cj *Object
	for i := range a.Workloads {
		if a.Workloads[i].Kind == "CronJob" {
			cj = &a.Workloads[i]
			break
		}
	}
	if cj == nil || cj.Status == nil {
		return Assessment{Health: HealthUnknown, Reason: "NoStatus", Detail: "schedule status was not readable"}
	}
	s := cj.Status

	if s.LastJobFailed {
		return Assessment{
			Health: HealthFailed,
			Reason: "LastRunFailed",
			Detail: "the most recent run failed",
		}
	}
	if s.Suspended {
		return Assessment{
			Health: HealthDegraded,
			Reason: "Suspended",
			Detail: "the schedule is suspended, so no runs are happening",
		}
	}
	if s.ActiveJobs > 0 {
		return Assessment{
			Health: HealthProgressing,
			Reason: "Running",
			Detail: fmt.Sprintf("%d run(s) in flight", s.ActiveJobs),
		}
	}
	if s.LastSuccessTime == nil && s.LastScheduleTime != nil {
		return Assessment{
			Health: HealthDegraded,
			Reason: "NeverSucceeded",
			Detail: "the schedule has fired but no run has succeeded",
		}
	}
	if s.LastSuccessTime == nil {
		return Assessment{
			Health: HealthUnknown,
			Reason: "NeverRun",
			Detail: "the schedule has not fired yet",
		}
	}
	return Assessment{
		Health: HealthHealthy,
		Reason: "LastRunSucceeded",
		Detail: fmt.Sprintf("last successful run %s", s.LastSuccessTime.UTC().Format(time.RFC3339)),
	}
}

func readyDetail(primary *Object) string {
	if primary == nil || primary.Status == nil {
		return "no problems detected"
	}
	if primary.Status.DesiredReplicas > 0 {
		return fmt.Sprintf("%d of %d replicas ready",
			primary.Status.ReadyReplicas, primary.Status.DesiredReplicas)
	}
	return "no problems detected"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

# Product spec

## Positioning

kubeside is a Kubernetes client for the person who ships the application, scoped
to their four daily questions, across every environment they deploy to.

One sentence for the README, the launch post, and every scope argument:

> The Kubernetes tool that shows your app, not your cluster.

## Principles

1. Subtract, do not add. Every feature request gets tested against
   [02-personas.md](02-personas.md). Anti-persona requests are declined in the
   issue, with a link.
2. Applications, not resource kinds. Navigation never starts with "pick a kind".
3. Time is a first-class axis. A tool showing only the present cannot answer the
   most common question.
4. Disable, never hide. Missing permission renders a disabled control naming the
   required verb.
5. Read-only by default, prod especially. Writing takes deliberate action.
6. Teach kubectl, do not replace it. Every view exposes the equivalent command.
7. Trust the numbers. A metric that could be wrong is not displayed.

## The four screens

Nothing else ships in v1.

### Screen 1: Apps

The landing view. One row per application, grouped by workload owner rather than
resource kind.

An application is a Deployment, StatefulSet, DaemonSet, CronJob, or Rollout, plus
everything reachable from it: owned ReplicaSets and Pods, the Services selecting
those pods, Ingresses routing to those Services, and every ConfigMap, Secret, PVC,
and ServiceAccount referenced by the pod spec.

Grouping is derived, with a documented precedence chain:

1. `app.kubernetes.io/name` plus `app.kubernetes.io/instance`
2. Helm release annotation (`meta.helm.sh/release-name`)
3. Argo CD instance label (`argocd.argoproj.io/instance`)
4. Owner reference chain up to the top-level controller
5. Workload name as a last resort

Each row carries: name, health, ready replicas over desired, image tag, age of
the current revision, restart count in the last 24 hours, and a sparkline of
recent restarts.

Health is derived and explainable. Clicking the badge shows why, naming the
condition and the probe.

### Screen 2: App detail with timeline

A horizontal time axis is the spine of the screen. Plotted on it:

- Deploys and rollouts, with image tag transitions
- Scale changes
- ConfigMap and Secret revisions referenced by the workload
- Pod restarts, with reason and exit code
- Probe failures
- OOMKills and evictions
- HPA scaling decisions
- Warning events

Below the axis, the current state: pods with status and age, the Service and
Ingress routing to them, and referenced config objects.

Selecting a range filters everything on the page to that window, including logs.
This is the incident workflow for Marina.

Every change carries an actor when derivable, from the managed-fields metadata,
so an out-of-band `kubectl edit` becomes visible rather than forensic.

The axis extends backwards through history reconstructed from the cluster, not
from anything kubeside recorded. Deploys come from ReplicaSets and
ControllerRevisions, releases from Helm release secrets, crashes from pod
`lastState`, and recent warnings from events still inside the apiserver TTL.
kubeside stores nothing on disk.

Two markers are mandatory, never decorative:

- Where the session began, labeled "kubeside started here"
- Where reconstruction ran out, labeled with the cause, for example "older
  rollouts pruned by revisionHistoryLimit"

Silence before those markers means "not known", and the UI says so. Rendering an
empty axis as though nothing happened would mislead exactly the person under the
most pressure.

### Screen 3: Resolved configuration

One table for the whole container. Columns: key, effective value, source, and
whether the value differs from the previous revision.

Sources merged:

- `env` inline values
- `envFrom` ConfigMap and Secret references
- Individual `valueFrom.configMapKeyRef` and `secretKeyRef`
- Downward API (`fieldRef`, `resourceFieldRef`)
- Container image defaults where discoverable
- Mounted volume paths for file-based config

Secret values stay masked, with a reveal action gated on the `get` verb for that
specific Secret, and every reveal recorded in the session timeline.

A second tab shows the same table diffed against another revision or another
environment, which is Rafael's job.

### Screen 4: Logs

Whole workload by default, every replica merged and time-ordered, with a color
key per pod. Per-pod is a filter, never the entry point.

Requirements:

- Follow mode with backpressure, so a chatty workload does not freeze the tab
- Regex filter and highlight
- Time-range binding to the timeline selection from Screen 2
- Previous-container logs for a crashed pod, in one click
- Copy a permalink reproducing the exact filter, workload, and window
- Download the current buffer

## Cross-cutting

Command palette on `cmd+k`. Every navigation and action reachable from the
keyboard, since Rafael arrives from k9s and will judge the tool on this within
thirty seconds.

Every view exposes "show kubectl", printing the equivalent command.

Environment switching and comparison are covered in
[04-multi-cluster.md](04-multi-cluster.md).

## Explicit non-goals for v1

No node view. No PersistentVolume or StorageClass browsing. No RBAC editor. No
CRD browser. No Helm chart management. No cost reporting. No topology graph. No
YAML editor beyond a read-only viewer with a copy action. No plugin system.

Each is a legitimate need belonging to an anti-persona or a later milestone.
Shipping any in v1 turns kubeside into a general-purpose dashboard.

## Anti-requirements

Behaviors to actively prevent, drawn from documented failures elsewhere:

| Anti-requirement | Source |
| --- | --- |
| Never hide controls based on RBAC | Headlamp HN thread, top comment chain |
| Never require Prometheus for basic metrics | Freelens #466, #627, #1670 |
| Never display a metric of uncertain correctness | Freelens #964, #1111, #1555, #1883 |
| Never make the user open one tab per replica for logs | Freelens #687, #1460 |
| Never issue a cluster-scoped list at startup | Headlamp #4051 |
| Never run kubeconfig exec plugins in a sandbox | Headlamp #1582 |
| Never leave secret values base64-encoded on screen | k9s #1017, #373 |
| Never write history to disk, and never render an unknown window as empty | Decision 2026-07-22, see 04-multi-cluster.md |

## Definition of done for v1

A developer with namespace-scoped read access in three clusters installs one
binary, runs it, and within ten seconds sees every app she owns across qa, stg,
and prod, with health, running version, and a timeline reaching back through the
last ten rollouts, assembled from the cluster and honest about where its
knowledge ends.
</content>

# The app list

The first question is whether your app is up. The app list answers it for every
environment your kubeconfig reaches, without you picking a resource kind first.

![The app list](../images/apps.png)

## What an app is

Kubernetes has no application object. kubeside builds one by walking controller
owner references from every object up to its top-level workload, then naming
that workload by the first rule that matches:

| Order | Rule | Shown as |
| --- | --- | --- |
| 1 | `app.kubernetes.io/instance`, then `app.kubernetes.io/name` | `recommended-labels` |
| 2 | `meta.helm.sh/release-name` annotation | `helm-release` |
| 3 | `argocd.argoproj.io/instance` label | `argocd-instance` |
| 4 | The workload's own name | `workload-name` |

The `GROUPED BY` column shows which rule produced each row, so a list that looks
wrong tells you why it looks wrong. A cluster full of `workload-name` is a
cluster whose labels carry no information, which is worth knowing.

A workload owned by something kubeside does not model, an operator's custom
resource for example, is marked `Kind via Owner` rather than hidden. It might
still be somebody's app.

## Health

Health is derived, and the row says what derived it:

| Glyph | Health | Example reason |
| --- | --- | --- |
| ■ | failed | `pod search-indexer-75f4d is in ImagePullBackOff` |
| ▲ | degraded | `1 replica failing readinessProbe on :8080/healthz` |
| ◇ | progressing | a rollout is in flight |
| ○ | unknown | `the schedule has not fired yet` |
| ● | healthy | ready equals desired |

Worst first: the two apps that need a human sit above the forty that do not.

A CronJob with no run yet is `unknown`, not `healthy` and not `failed`. Reading
"nothing has happened" as "everything is fine" is exactly the mistake this
column exists to avoid.

## Metrics

CPU and memory come from metrics-server when it is installed, from Prometheus
when you point at one, and from nothing when neither exists. In that last case
the columns disappear and the header says why:

```
metrics: unavailable · metrics-server is not installed (metrics.k8s.io is not registered)
```

A zero would be a reading nobody took.

## Partial reads

If a kind could not be listed, it is named:

```
partial: CronJob
```

That is the difference between "this cluster has no CronJobs" and "your role
cannot list CronJobs here". Only one of those is a fact about the cluster.

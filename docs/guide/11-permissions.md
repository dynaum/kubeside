# Permissions

kubeside asks your cluster what you may do and renders the answer. It never
assumes, and it never hides a control because the answer was no.

## What it reads

Ordinary read access to the kinds it groups: Deployments, StatefulSets,
DaemonSets, CronJobs, Jobs, Pods, ReplicaSets, ControllerRevisions, Events, and
Services. A read-only role scoped to your namespaces is enough for everything on
the four screens.

Two reads are optional and degrade cleanly:

| Read | Without it |
| --- | --- |
| `list` on namespaces | Falls back to your context's namespace, and says so. |
| `list` on secrets | Helm history becomes a labelled gap on the timeline. |

## What it checks before offering a control

Each of these is resolved per namespace with a SelfSubjectAccessReview, the same
question `kubectl auth can-i` asks:

| Control | Verb |
| --- | --- |
| Logs | `get` on `pods/log` |
| Exec | `create` on `pods/exec` |
| Port-forward | `create` on `pods/portforward` |
| Reveal a secret value | `get` on that specific `secret` |
| Restart a workload | `patch` on `deployments` |
| Delete a pod | `delete` on `pods` |

## Disabled, never hidden

A refused control stays on screen, disabled, naming the verb it needs:

```
Exec   needs create on pods/exec in team-a
```

Hiding it would teach you nothing and leave you guessing whether the feature
exists. Naming the verb gives you the exact sentence to send your platform team.

Answers are cached per context for the session. Failures are never cached: a
check that could not run is not an answer, and it says the check failed rather
than that access was refused.

## What it never does

It does not run kubeconfig exec plugins in a sandbox, it does not proxy your
credentials anywhere, and the browser never talks to an apiserver. Every request
to a cluster is made by the kubeside process on your machine.

# The timeline

The second question is what changed and when. Kubernetes already keeps that
history; nothing assembles it. kubeside does, on demand, with no storage layer.

![Timeline and horizons](../images/app-detail.png)

## Where it comes from

| Source | Gives |
| --- | --- |
| ReplicaSets | Deployment rollouts, with the image each revision ran |
| ControllerRevisions | StatefulSet and DaemonSet revisions |
| Helm release Secrets | Chart releases, versions, and their order |
| Pod `lastState` | Restarts, OOM kills, exit codes |
| Events | Probe failures, scheduling problems, image pulls |
| The live session | Everything observed since kubeside started watching |

## Horizons

Every lane says where its knowledge ends, and why:

```
replicaset · older rollouts pruned by revisionHistoryLimit; revision 11 is the
             oldest the cluster still holds
event      · older events have expired from the apiserver, which keeps roughly
             an hour by default
session    · kubeside started watching here; anything before this comes from
             the cluster's own history
```

The area beyond a horizon is hatched, never blank. An empty axis would read as
a period when nothing happened, which is the single most misleading thing a
history view can do.

A source your role cannot read is a labelled gap, not a silent omission:

```
helm · unavailable · needs read access to secrets in this namespace
```

## Who did it

Changes carry an actor when one can be attributed, read from `managedFields` on
the object that changed, within a minute of the change. `kubectl` next to a
revision is the out-of-band change worth seeing; a CI service account next to
one is the normal path.

Attribution is best-effort and says so. Status-subresource updates are skipped,
because the controller that wrote them is not who caused the change.

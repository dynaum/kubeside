# When things are missing

Most of what follows is not an error. A developer's day is full of these, and
each one has a wrong behaviour that looks like a right one.

## A cluster shows a state instead of apps

```
unreachable · dial tcp 10.0.0.1:443: i/o timeout
```

VPN, DNS, or the cluster is down. kubeside shows no apps for it rather than an
empty list, because an empty list is a claim about the cluster's contents.

One cluster failing never affects another. Every context has its own connection
and its own state.

## A cluster says unauthorized

```
unauthorized · the server has asked for the client to provide credentials
```

Your session expired. Re-run whatever authenticates you (`aws sso login`,
`gcloud auth login`) and reconnect. This is deliberately a different state from
`unreachable`, because the next action is different.

## The scope says one namespace

```
scope: namespace team-a · listing namespaces was refused
```

Your role is namespace-scoped, which is normal. No Kubernetes API enumerates the
namespaces a user may access, so a refused cluster-scoped list is an answer, not
a failure. To see more, name them in the config:

```yaml
defaults:
  namespaces: [team-a, team-b]
```

## A kind is named as partial

```
partial: CronJob
```

That kind could not be listed. A row might be missing. This is named precisely
so you do not conclude the cluster has no CronJobs.

## Metrics columns are gone

```
metrics: unavailable · metrics-server is not installed
```

Install metrics-server, point at Prometheus in the config, or read the rest
without them. A zero is never shown for a reading nobody took.

## The timeline starts recently

The cluster is the archive, and it prunes. `revisionHistoryLimit` removes old
ReplicaSets, and the apiserver expires events after roughly an hour. The horizon
marks exactly where the reconstruction ends. Anything earlier is genuinely gone
from the cluster, and no tool without a database can show it.

## A control is disabled

It names the verb it needs. That sentence is the request to send to whoever
manages your RBAC. See [permissions](11-permissions.html).

## Logs stop

Look for the edge. `ended` means the container exited, `gone` means the pod was
deleted, `error` carries the reason. A log view that simply went quiet would be
the failure worth worrying about.

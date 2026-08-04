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

Install metrics-server, or read the rest without them. A zero is never shown for
a reading nobody took. `metrics: prometheus` is accepted in the config file and
not implemented, so it reports unavailable rather than pretending.

## A usage number ends in an asterisk

Fewer of the app's pods reported than it has. Hover for the count. The usual
cause is a replica that is not running: a crash-looping or pending pod reports
nothing, so the total covers the replicas still up. It is a real number about
part of the app, not the app's usage.

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

## Is there a TUI, like k9s?

No, and this is a decision, not an oversight. Asked on the same
[Show HN thread](https://news.ycombinator.com/item?id=49139573) that raised the
Argo CD question.

The four questions kubeside answers are layout problems before they are
anything else. A timeline needs a horizontal axis with markers on it. A
side-by-side config diff needs two columns a reader can scan against each
other. A promotion matrix needs a grid with color and drift indicators. A
monospace grid renders none of them well, and forcing them into one would
mean designing worse versions of screens that already work in a browser.

k9s also already owns the terminal, and it does that job well. Competing with
it on keyboard speed would be a fight against a tool people already like,
over ground kubeside does not need.

If your kubeconfig is not local, for example a shared cluster with no kubectl
access from your machine, `--serve` mode is the answer. It runs the same
binary and the same screens against the cluster directly, over OIDC. It is
listed under Beyond v1 in
[the roadmap](https://github.com/dynaum/kubeside/blob/main/docs/06-roadmap.md)
and has not shipped yet.

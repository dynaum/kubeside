# Install and run

kubeside is one binary. It has no runtime, no installer, no agent in your
cluster, and no setup step. If `kubectl` works on your machine, kubeside works.

## Install

```
brew install dynaum/tap/kubeside
```

Or download an archive for macOS, Linux, or Windows from the
[releases page](https://github.com/dynaum/kubeside/releases) and put the binary
on your `PATH`.

Until the first release is tagged, build it:

```
git clone https://github.com/dynaum/kubeside && cd kubeside
npm --prefix web ci && npm --prefix web run build   # the UI is embedded, so it goes first
go build ./cmd/kubeside
./kubeside
```

## Run

```
kubeside
```

That starts a server on `127.0.0.1:7654` and opens your browser. The server
holds the credentials and talks to your clusters; the browser only ever talks to
`127.0.0.1`.

The page loads nothing from anywhere else. Stylesheets, scripts and the IBM Plex
typeface all come out of the binary, so kubeside looks and works the same on a
VPN-only laptop, behind egress rules, or on a network that has never heard of
your machine. A test asserts it, because a missing request is invisible on a
developer machine with open egress.

Nothing is written to disk while it runs. No database, no cache, no history
file. Stop the process and nothing is left behind.

## Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `--port` | `7654` | Port for the local UI. |
| `--no-open` | off | Start the server but do not open a browser. |
| `--print` | off | Print the app list to the terminal and exit, instead of opening the UI. |
| `--context` | all | Comma-separated context names to use. Everything else in the kubeconfig is ignored. |
| `--kubeconfig` | discovery | An explicit kubeconfig path, overriding `KUBECONFIG` and `~/.kube/config`. |
| `--config` | discovery | An explicit kubeside config path. See [the config file](10-config-file.html). |
| `--profile` | unset | Sets `AWS_PROFILE` for exec credential plugins. Your kubeconfig is never edited. |
| `--timeout` | `15s` | Per-cluster connect and fetch timeout. |
| `--version` | | Print the version and exit. |
| `--help` | | Print this table and exit 0. |

`--serve`, the in-cluster team dashboard, is not a flag yet. It is
[beyond v1](https://github.com/dynaum/kubeside/blob/main/docs/06-roadmap.md).
Until it exists, kubeside rejects it rather than listing an option that only
returns an error.

## The terminal check

`--print` answers "does grouping produce apps I recognise" without opening
anything:

```
$ kubeside --print
kubeside v1.0.0
2 contexts from /Users/you/.kube/config

── kind-kubeside-demo  [live]  kind-kubeside-demo · high risk
   scope: cluster-wide
      NAMESPACE    APP             KIND        READY  OBJECTS  GROUPED BY          WHY
   ■  team-b       search-indexer  Deployment  0/2    4        recommended-labels  pod search-indexer-75f4dc7c5-dlg8c is in ImagePullBackOff
   ●  team-a       checkout        Deployment  4/4    12       recommended-labels
   ●  team-b       billing         CronJob     —      3        workload-name

   9 apps
   health: healthy 8, failed 1
   grouped by: owner-chain 1, recommended-labels 2, workload-name 6
```

The `grouped by` tally is the diagnostic worth reading. A cluster grouping
mostly by `workload-name` is one where the label conventions earned nothing, and
that is a finding about the cluster, not a bug in the tool.

It is also the fastest way to see whether a cluster is reachable at all, since
each context prints its own state rather than one shared success or failure.

## Uninstall

Delete the binary. If you created a config file, delete
`~/.config/kubeside/config.yaml`. There is nothing else.

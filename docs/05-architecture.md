# Architecture

## Shape

One Go binary. Embedded frontend. Two run modes from the same artifact.

```
kubeside              local mode: HTTP on 127.0.0.1:7654, opens a browser
kubeside --serve      in-cluster mode: same server, OIDC auth, Helm-deployable
```

Local mode ships first. In-cluster mode inherits the entire authentication
problem documented in [01-problem.md](01-problem.md) and waits until local mode
is solid.

## Why not the alternatives

| Option | Rejected because |
| --- | --- |
| Terminal UI | The four gaps are layout problems: timeline, side-by-side config diff, promotion matrix. A monospace grid cannot render them. k9s also owns the terminal, and competing on keyboard speed against a beloved incumbent is a losing fight. |
| Electron | What Freelens ships, and the exact axis on which reviewers rate it below Aptakube. Chromium per window on a laptop already running three of them. |
| Tauri | Fixes weight, introduces webview divergence across macOS, Windows, and Linux. A permanent tax when design quality is the differentiator. |
| Browser-only, apiserver direct | Forces CORS workarounds, exposes credentials to the page, and blocks kubeconfig exec plugins entirely. |

Local server plus browser rendering keeps the terminal launch developers expect,
gives full design capability, runs credential plugins natively, and turns
`--serve` mode into a flag instead of a rewrite.

## Backend

Go. `client-go` with informers.

### Layers

```
cmd/kubeside          entrypoint, flags, browser launch
internal/clusters     ClusterManager, per-context connection lifecycle
internal/informers    typed informer factories, tiered watch scoping
internal/apps         grouping engine: resources to applications
internal/timeline     history reconstruction, event ingestion, actor attribution
internal/config       the optional config.yaml: environments, namespace axis
internal/resolved     resolved configuration merge with provenance
internal/logs         multi-pod stream merge, backpressure, ring buffer
internal/forward      port-forward lifecycle, loopback-only listeners
internal/rbac         per-context permission resolution, cached for the session
internal/metrics      source interface: metrics-server, prometheus, none
internal/session      in-memory ring buffers, eviction, horizon tracking
internal/api          HTTP + websocket handlers
web/                  React frontend, embedded via embed.FS
```

### Cluster manager

`map[contextID]*ClusterConn`, each holding its own informer factory, REST client,
permission cache, and circuit breaker. One goroutine per connection. A dead
cluster never blocks a request for another.

`contextID` derives from cluster UID first and kubeconfig name second, so a
context rename in kubeconfig preserves stored history.

Watch tiers, per [04-multi-cluster.md](04-multi-cluster.md):

| Tier | Informers | When |
| --- | --- | --- |
| Active | Deployments, StatefulSets, DaemonSets, ReplicaSets, Pods, Services, Ingresses, ConfigMaps, Secrets metadata, Events, HPAs | Environment currently on screen |
| Background | Deployments, StatefulSets, DaemonSets, Events | Other environments in the promotion view |
| Idle | None, cache retained | No view referenced for 15 minutes |

Tiers move on attention alone. Reading an environment promotes it to active;
two minutes without a read demotes it to background; fifteen drops the
connection and keeps the snapshot. Nothing announces that a tab closed, because
nothing has to: the absence of reads is the signal.

Until informers land, "active" means the read includes pods and "background"
means it does not. Pods are the most expensive collection in any real cluster
and the one only the environment on screen needs, so a background read skips
them and names them as unread rather than implying those apps have no replicas.

A read where every kind failed produces an empty list indistinguishable from an
empty cluster, so it does not replace what was last known: the previous snapshot
renders with the failures named. Reaping drops the connection and the cached
permissions, since new credentials on reconnect can mean different answers, but
never the snapshot.

Secrets use a metadata-only informer. Values are fetched on demand, per key, with
an explicit permission check. Secret values never enter the watch cache.

Three on-demand paths sit outside the tiers entirely, all memoized for the
session: timeline reconstruction, secret value reveals, and pod metadata for
the promotion view's digest comparison, which needs `imageID` from pod status
in environments whose background tier watches no pods.

### Grouping engine

Pure function from a resource set to applications, using the precedence chain in
[03-product-spec.md](03-product-spec.md). Deterministic and unit-testable against
fixture clusters, since this is the core abstraction and regressions here break
everything downstream.

### Timeline

Three ingestion paths, in this order.

1. Reconstruction from the cluster, on demand. Kubernetes already retains
   substantial history and no tool assembles it. See the next section.
2. Kubernetes Events, watched live for the duration of the session.
3. Change detection over informer deltas. Each workload update diffs against the
   previous observed revision in the session buffer. Meaningful transitions
   become timeline entries: image change, replica change, config reference
   change, probe change.

Path 1 fills the axis before the session began. Paths 2 and 3 extend it forward
while kubeside runs.

Actor attribution reads `metadata.managedFields`, mapping the field manager to a
label: `kubectl`, `helm`, `argocd`, `autoscaler`, `controller`, or a raw manager
name. A `kubectl` manager touching prod is the out-of-band change Rafael wants
surfaced.

Matching is by time. managedFields records when each manager last wrote, so a
rollout that happened within a minute of a manager's write was that manager's
doing. Two limits are respected rather than papered over: only the latest entry
per manager survives, so attribution reaches the most recent change and older
rollouts carry no actor rather than a guessed one; and writes to the `status`
subresource are skipped, because a controller reporting on a change is not the
change, and attributing to it would bury whoever made it.

### Metrics

An interface with three implementations, selected by probe at connection time:

```go
type Source interface {
    PodMetrics(ctx context.Context, ns string, sel labels.Selector) ([]PodSample, error)
    Available() bool
    Name() string
}
```

Probe order: `metrics.k8s.io` availability, then a configured Prometheus
endpoint, then none. `none` renders an explicit empty state naming what to
install. Never a zero, never a guess.

Every sample carries its source in the API response, and the UI labels it. The
Freelens defect class where a value silently doubles becomes visible instead of
mysterious.

### History, without storage

kubeside writes nothing to disk. No database, no cache file, no local record of
what happened. When the process exits, everything it observed is gone.

This is a product decision, not a limitation to work around. A tool holding
cluster credentials earns trust with a one-sentence guarantee, and "writes
nothing, sends nothing" is that sentence. It also keeps `--serve` mode from
becoming a system of record with backup, retention, and audit obligations
kubeside is not designed to carry.

The timeline survives because Kubernetes already stores history. Nobody assembles
it, which is the actual gap.

| Source | Recovers | Typical depth |
| --- | --- | --- |
| ReplicaSets owned by a Deployment | Deploy history with image tags and timestamps | `revisionHistoryLimit`, default 10 |
| ControllerRevisions | Same for StatefulSets and DaemonSets | Default 10 |
| Helm release secrets (`sh.helm.release.v1.*`) | Release history with chart version, timestamp, and values | Default max history 10 |
| Argo CD application status | Sync history with git revisions | Per Argo config |
| Pod `status.containerStatuses[].lastState.terminated` | Previous crash reason and exit code | Last termination |
| Pod `restartCount` | Restart totals per container | Pod lifetime |
| `deployment.kubernetes.io/revision` annotations | Rollout ordering | Matches ReplicaSets |
| Events in etcd | Recent warnings, probe failures, evictions | apiserver `--event-ttl`, default 1h |

Each source degrades independently, and degradation is labeled. Helm release
secrets need `get` on secrets in the namespace, which read-only prod roles
often exclude, so the richest source fails for exactly the personas in exactly
the environment where the timeline matters most. When a source is forbidden
the timeline renders from the others and marks the gap, for example "Helm
history unavailable: needs read access to secrets". Never an error state, never
silent absence.

Reconstruction runs on demand when an app detail view opens, not at startup, so
launch stays cheap. Results are memoized for thirty seconds rather than for the
whole session: until live extension lands, a cache that never expires would hide
a rollout that happened while the window was open, and a timeline that quietly
lags is worse than one that costs a read.

Each source is read independently and a failure in one becomes a labeled gap
rather than an error, so a read-only prod role that cannot read secrets still
gets rollouts, crashes, and warnings. The event horizon is dated from the oldest
event in the namespace, not the oldest event for the app: the apiserver's
retention is what ends there, and dating it from one quiet app would claim a cut
that is not real.

An important property: two developers opening kubeside see the same
reconstructed timeline, because both read the same cluster. A local database
could never guarantee that.

### The session buffer

Live observations accumulate in memory for the duration of the process.

- Ring buffer per app, capped by entry count and by total bytes
- Eviction is oldest-first, and eviction is visible rather than silent
- Nothing is written anywhere on eviction
- 2000 entries and 4MB per app, 100MB across every app

What fills it is the delta path already running for the app list: every row that
changed between two reads is reported, and a health transition becomes an entry.
Replica counts are dropped, because they churn through every rollout and a
timeline of them buries the one line that matters.

Live entries merge with reconstruction on every read rather than being cached
with it. Reconstruction is memoized for thirty seconds; a transition that
happened ten seconds ago must not wait for that to expire.

The two halves carry different horizons, and they say different things. An
evicted buffer is a cut in something that was known. A session start is simply
where kubeside began watching, with the cluster's own history behind it.

Retention did not disappear, it moved from disk to RAM with a smaller budget. The
cap is enforced from the first release rather than discovered at 400MB.

### Horizon honesty

The timeline always renders where its knowledge ends.

- A marker labeled "kubeside started here" at session start
- A second marker where reconstruction depth runs out, labeled with the reason,
  for example "older rollouts pruned by revisionHistoryLimit"
- Events beyond the apiserver TTL are marked absent, never rendered as quiet

A timeline that silently shows a partial picture during an incident is worse than
one that admits its edges. Marina at 03:00 must never mistake "nothing happened"
for "kubeside was not watching".

### What is genuinely unavailable

Stated plainly so nobody expects otherwise:

- ConfigMap and Secret change history before the session, since Kubernetes keeps
  no revision trail for them unless the team uses immutable versioned names
- Events older than the apiserver TTL
- Any comparison against yesterday

The first is the real cost. A config change three hours before launch is a common
root cause and kubeside will not see it. Partial mitigation: the current
ConfigMap carries `metadata.resourceVersion` and a last-applied annotation when
`kubectl apply` was used, which at least dates the most recent change.

### Transport

REST for reads and mutations. One websocket per browser tab, carrying deltas.

Delta protocol: the frontend subscribes to a view, the backend pushes typed
patches. The browser never polls and never talks to an apiserver.

A subscription answers with one snapshot and then patches carrying only what
moved: rows added, rows changed, rows removed by key, plus view-level metadata
whenever the connection state, the readable scope, or the unreadable kinds
change. Absent means unchanged, never empty. Each message carries a sequence
number, so a client that sees a gap resubscribes rather than rendering a view it
cannot trust.

What fills a patch is currently a periodic read per watched view, not an
informer. The wire protocol is deltas from the first commit, so the tiered
watches below replace the source without moving a line of client code. Nothing
is read at all unless a tab is subscribed: attention is what makes a connection
active, which is also what keeps a kubeconfig with thirty contexts from waking
thirty apiservers because a window opened. A tab that stops draining its queue
is disconnected and resyncs on reconnect, rather than being allowed to stall the
feed every other tab shares.

Log streams multiplex on the same websocket, tagged per pod, with a server-side
ring buffer of 10k lines per workload and backpressure toward the client so a
chatty deployment cannot freeze a tab.

The unit is the workload: every replica is merged into one time-ordered stream,
and per-pod is a filter on that stream rather than a second way in. Lines are
batched before they reach the wire, since a workload emitting thousands of lines
a second must not cost one frame per line. The ring counts what it drops, and
that count travels with every batch: a buffer that quietly loses lines makes a
chatty workload look calm.

Which pods back a workload comes from the grouping engine, re-read on a timer,
which is how a rollout's new replicas join a stream already on screen. A label
selector would be a second answer that disagrees with the engine on exactly the
workloads where grouping was hard.

Availability edges travel with the lines and render where they happened: where
a stream begins, that previous-container output reaches exactly one restart
back, that a pod left the workload, and that a container stopped rather than
went quiet. A gap the tool cannot explain is a gap the developer will misread as
silence.

A restart ends a container's log stream, so the reader reconnects and resumes
after the newest line it holds, marking the boundary. Treating the end of a
stream as the end of the story would render a crash loop, the case this screen
exists for, as a quiet period.

### Kubeconfig

kubeside reads the developer's existing kubeconfig. No import step, no separate
credential store, no setup wizard. If `kubectl` works, kubeside works.

What is honored, in the same way `client-go` honors it for kubectl:

| Element | Behavior |
| --- | --- |
| `KUBECONFIG` chain | Multiple colon-separated files merged in precedence order |
| `~/.kube/config` | Default when `KUBECONFIG` is unset |
| `--kubeconfig` flag | Overrides both |
| Every context | All of them load, not only `current-context`. Multi-environment is the point. |
| `current-context` | Selects the environment focused on launch |
| Per-context `namespace` | Seeds the default namespace filter for that context |
| Exec credential plugins | Run as native child processes: `aws eks get-token`, `gke-gcloud-auth-plugin`, `kubelogin`, `tsh`, anything else |
| `proxy-url` | Honored per cluster |
| Client certs, tokens, `auth-provider` | Honored as-is |
| Custom CA, `insecure-skip-tls-verify` | Honored, and the insecure case renders a visible warning on the panel |
| Impersonation (`as`, `as-groups`) | Honored |

Three guarantees, stated because trust in a tool holding cluster credentials is
the whole game:

1. kubeside never writes to kubeconfig. Not to add a context, not to refresh a
   token, not to set `current-context`. The file is read-only input.
2. kubeside never copies credentials anywhere. Tokens live in process memory for
   the session and die with the process. Nothing reaches disk, since kubeside
   writes no files at all beyond the config the user edits themselves.
3. kubeside never sends anything to a remote service. No telemetry in v1, and any
   later telemetry ships opt-in with the payload documented.

Environment classification from context names is described in
[04-multi-cluster.md](04-multi-cluster.md). A file watcher on the kubeconfig
picks up newly added contexts without a restart.

### Authentication

Local mode: the user's kubeconfig, full stop. Exec credential plugins run as
native child processes with the real environment, so `aws eks get-token`, `gke-
gcloud-auth-plugin`, and `kubelogin` behave exactly as they do for kubectl. This
sidesteps Headlamp's Flatpak sandbox failures entirely.

Token refresh is per context with its own lifecycle. An expiring SSO session
prompts inline on the affected panel, never in a modal covering the app.

The local server binds `127.0.0.1` only, requires an `Origin` check, and carries
a per-session token in the URL the browser opens with. A local HTTP server
holding cluster credentials is a real attack surface and gets treated as one.

In-cluster mode: OIDC with user impersonation. No shared service account with
broad rights.

## Frontend

React 19, TypeScript, Vite.

| Concern | Choice | Reason |
| --- | --- | --- |
| Styling | Tailwind plus Radix primitives, custom design system on top | Headlamp uses MUI and looks like every other MUI app. Design is the differentiator, so no component library aesthetic. |
| Server state | TanStack Query, hydrated by websocket deltas | Most data is a subscription, not a fetch |
| View state | Zustand | Small, no boilerplate |
| Tables | TanStack Table plus TanStack Virtual | Pod lists reach thousands of rows |
| Time series | uPlot | Recharts falls over at the density the timeline needs |
| Timeline and diff | visx | Custom marks on a shared time axis |
| Terminal | xterm.js over the websocket | Exec sessions |
| Routing | TanStack Router | Typed params, and every view needs a permalink |

Build output embeds through `embed.FS`. One file ships.

### Design system

Defined before the first screen, not retrofitted. Tokens for color, spacing,
type scale, and motion. Both themes from day one, since dark-theme contrast bugs
are a recurring complaint in every competing tracker.

Environment color is a system token, not a decoration. Prod red propagates to
borders, headers, and confirmation dialogs from a single source.

Accessibility is a token-level constraint, not a review item. Every color pair
in both themes meets WCAG AA contrast, enforced in CI against the token file.
Full keyboard operability beyond the command palette, with focus states
specified per component. Dark-theme contrast failures are a recurring
complaint across all three competing trackers, and this plan cites those bugs
as ammunition, so shipping one would undercut the entire design argument.

## Performance budget

Numbers, because "feels fast" is not testable.

| Metric | Budget |
| --- | --- |
| Cold start to first paint | 300ms |
| First environment's app list rendered | 1.5s |
| Reconcile against a live 500-pod cluster | 3s |
| Timeline reconstruction for one app | 800ms |
| Log stream first line | 400ms |
| Memory, three connected clusters, 8-hour session | 350MB |
| Session buffer cap, all apps combined | 100MB, oldest-first eviction |
| Binary size | 40MB |
| Apiserver load | Zero polling. Informers only. |

With no cache on disk, launch is bounded by the slowest cluster connection. The
promotion view fills in progressively, per environment, each cell showing its own
loading state. A prod cluster behind a VPN never blocks qa from rendering.

Environment panels render in `current-context` order, so the environment the
developer works in most appears first.

## Distribution

- GoReleaser for macOS (arm64, amd64), Linux, Windows
- Homebrew tap, reusing the existing `dynaum/homebrew-tap`
- `go install` for the Go-native audience
- Helm chart for `--serve` mode, published later
- Apache-2.0

## Testing

- Grouping engine against fixture manifests, table-driven. The highest-value
  tests in the project. Fixtures include a cluster with no recommended labels
  and no Helm, where the owner-chain and name fallbacks carry everything,
  since entire shops run without labels and the last-resort path is
  first-class there. Also both collision cases: same name in two namespaces of
  one cluster, and cross-environment namespace conventions.
- envtest for informer and permission behavior against a real apiserver.
- kind-based integration suite covering three simulated environments, exercising
  the promotion view and cross-environment diff.
- Playwright for the four screens, plus a screenshot diff gate, since visual
  regression is a product risk when design is the differentiator.
- Explicit test for degraded modes: unreachable cluster, expired credential,
  namespace-scoped RBAC, and no metrics source.

## Risks

| Risk | Mitigation |
| --- | --- |
| Radar reaches feature parity from a topology angle | Compete on the developer scope and the promotion view, not on graphs |
| Browser-in-a-tab reads as less premium than native | Sub-300ms cold start, command palette, deliberate design |
| Informer memory across three clusters | Watch tiering and idle disconnect, measured against the budget above |
| Grouping heuristics misfire on unusual setups | Config override, and the derivation is always visible and explainable |
| Scope creep toward the operator | Anti-personas documented, requests declined with a link |
</content>

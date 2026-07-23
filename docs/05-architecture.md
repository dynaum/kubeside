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
internal/config       resolved configuration merge with provenance
internal/logs         multi-pod stream merge, backpressure, ring buffer
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

Secrets use a metadata-only informer. Values are fetched on demand, per key, with
an explicit permission check. Secret values never enter the watch cache.

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
label: `kubectl`, `helm`, `argocd`, `hpa`, or a raw manager name. A `kubectl`
manager touching prod is the out-of-band change Rafael wants surfaced.

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

Reconstruction runs on demand when an app detail view opens, not at startup, so
launch stays cheap. Results are memoized for the session.

An important property: two developers opening kubeside see the same
reconstructed timeline, because both read the same cluster. A local database
could never guarantee that.

### The session buffer

Live observations accumulate in memory for the duration of the process.

- Ring buffer per app, capped by entry count and by total bytes
- Eviction is oldest-first, and eviction is visible rather than silent
- Nothing is written anywhere on eviction

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

Log streams multiplex on the same websocket, tagged per pod, with a server-side
ring buffer of 10k lines per workload and backpressure toward the client so a
chatty deployment cannot freeze a tab.

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
  tests in the project.
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

# Roadmap

Milestones ordered so each one is independently useful. No milestone exists
purely as scaffolding for the next.

## M0. Spike, one week

Prove the two assumptions everything else rests on.

- Informers across three kubeconfig contexts in one process, with lazy connect
  and idle disconnect.
- Grouping engine over a real cluster, producing an app list a human recognizes.

Exit criteria: `kubeside` prints a correct app list for three contexts, in under
500ms from cache, in a terminal. No UI.

Kill criterion: if grouping produces results developers do not recognize as their
apps, the whole product thesis is wrong and the spike ends here.

## M1. Apps and logs

The first release worth installing. Serves Ana.

- Screen 1: app list, single environment, health derived and explainable
- Screen 4: whole-workload log streaming, merged, filter, follow, permalink
- Metrics source interface with metrics-server and none
- Design system tokens, both themes
- Command palette
- macOS and Linux binaries, Homebrew tap

Exit criteria: Ana reads logs for a three-replica deployment without opening a
second tab.

## M2. Time

The differentiator. Serves Marina.

- History reconstruction from ReplicaSets, ControllerRevisions, Helm release
  secrets, pod `lastState`, and events
- Live event ingestion and change detection over informer deltas
- Screen 2: app detail with the timeline
- Horizon markers: session start, and where reconstruction ran out, with cause
- Actor attribution from managedFields
- Time-range selection binding logs to the timeline window
- Session ring buffers with a byte cap and oldest-first eviction

No storage layer. kubeside writes nothing to disk, which removes SQLite, schema
migrations, retention policy, and the whole system-of-record question from the
project. Decision recorded 2026-07-22 in
[04-multi-cluster.md](04-multi-cluster.md).

Exit criteria: opening kubeside cold against a cluster it has never seen produces
a populated timeline covering the last ten rollouts, with its knowledge boundary
visibly marked.

## M3. Environments

Serves Rafael and Bruno. The multi-cluster work in
[04-multi-cluster.md](04-multi-cluster.md).

- Environment model, inference from context names, config file
- Promotion view with drift detection and digest comparison
- Per-environment RBAC resolution
- Prod guardrails: color, write policy, typed confirmation, break-glass
- Watch tiering and per-context circuit breakers

Exit criteria: "is the fix in prod" answers in one glance, and no single control
mutates more than one environment.

## M4. Configuration

Completes the four screens. Serves Ana and Rafael.

- Screen 3: resolved configuration with provenance per row
- Secret masking with per-key permission gating and reveal logging
- Diff against a previous revision
- Cross-environment config diff with expected, drift, and missing classification

Exit criteria: "works in stg, fails in prod" resolves without reading YAML.

## M5. v1.0

Polish and the credibility work.

- Windows binary
- Playwright suite with screenshot diff gate
- Degraded-mode tests: unreachable cluster, expired credential, no metrics, no
  cluster-scope permission
- Exec through xterm.js, permission-gated
- Documentation site
- Launch: Show HN, r/kubernetes, CNCF Slack

## Beyond v1

Not committed. Listed so the ordering conversation happens once.

- `--serve` in-cluster mode with OIDC and a Helm chart
- Prometheus metrics source
- Argo CD and Flux awareness, showing desired versus live state
- Git SHA resolution from image labels for the promotion view
- Port-forward management
- Plugin system, and only if a concrete second-party need appears first

## Permanently out

Node view. PersistentVolume and StorageClass browsing. RBAC editing. CRD
browsing. Helm chart management. Cost reporting. Alerting. Each belongs to an
anti-persona in [02-personas.md](02-personas.md).
</content>

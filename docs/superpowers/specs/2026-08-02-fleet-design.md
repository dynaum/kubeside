# Fleet: one app across every cluster

Design spec, 2026-08-02. Source: the Show HN thread for kubeside,
[item 49139573](https://news.ycombinator.com/item?id=49139573), 25 points and 8
comments.

## Why

Two people asked the same question independently in the thread.

maxweisel runs singular apps deployed across multiple clusters and wants to see
"if all clusters are running the latest version." The maintainer confirmed the
same pain from the other side: clusters per team, plus uat, plus prod, and the
recurring need to check one service across all of them.

A third commenter read the README tagline, "Your apps, across every cluster,
without thinking in ReplicaSets," and assumed the capability already shipped.

It has not, and the current behavior is worse than absent.

## The defect this uncovers

`docs/04-multi-cluster.md:20` states one environment maps to one or more
contexts, and the promotion view "aggregates them with per-context detail on
expansion." Line 334 repeats it as a handled failure mode for an environment
holding 40 contexts.

`internal/promotion/promotion.go:144` builds its per-environment map with
`byEnv[in.Env] = in`. Last write wins. A `prod` environment holding
`prod-us-east` and `prod-eu-west` renders one cell showing whichever context
arrived last, and discards the other silently.

`promotion.Env` carries a single `Context string`. `promotion.Instance` carries
no context at all. The aggregation was specified and never built.

The overwrite is the second place the data dies. The first is
`internal/api/service.go:1006`:

```go
// Two contexts bound to one environment would produce two identical
// columns; the first one wins, which is what the binding is for.
if seen[env.Name] {
	continue
}
```

A second context in an environment is skipped before it is connected or
fetched. Its assumption, that two contexts in one environment are identical, is
exactly what a team running prod in three regions disproves. Both layers get
fixed, and the service layer comes first, since the matrix cannot render data
nobody gathered.

So the promotion view answers "is the fix in prod" with a confident version it
did not verify. Compare that against the rule at
`docs/04-multi-cluster.md:216`, where unauthorized renders distinctly from
absent because "I cannot see it" and "it is not there" are different facts.
Reporting one cluster's version as the environment's version breaks the same
principle in a way harder to notice.

## Scope

In:

- A fleet screen: one app, one row per cluster.
- The promotion matrix refusing to collapse a disagreement.
- Argo CD entering the tool comparison.
- The TUI request declined in writing.

Out:

- `--serve` in-cluster mode. It stays under Beyond v1 in
  `docs/06-roadmap.md:111`. The thread raised demand for it. Demand does not
  shrink the OIDC and Helm work it requires.

## Anti-persona check

The fleet screen is app-scoped across clusters. It adds no node view, no
capacity data, no RBAC visualization, and no cost attribution. It clears A1, A2,
and A3 in `docs/02-personas.md:249`. It serves Rafael and Bruno, the same
personas the promotion view serves.

## Model

New package `internal/fleet`. One type carries one app in one cluster.

```go
// Placement is one app as it exists in one cluster.
type Placement struct {
	Context   string // kubeconfig context name, personal and unstable
	ClusterID string // API server URL, from kubeconfig.Context.Server
	Env       string // resolved environment name, "unclassified" when unmatched
	Namespace string

	State  string // present | absent | denied | unreachable | pending
	Reason string

	Image  string
	Tag    string
	Digest string
	// DigestPending marks a placement whose digest has not arrived. A missing
	// digest never reads as a match.
	DigestPending bool

	Health     string
	Ready      string
	RevisionAt string
}
```

The view derives two facts across the set: the newest tag among present
placements, and every placement behind it. "Are all clusters on the latest
version" reduces to one count.

Tag ordering already exists and is tested in `promotion.compareTags`. Fleet
reuses it. Two comparators disagreeing about which version is newer would be the
worst defect this feature can carry, so `CompareTags` and the namespace-token
stripping inside `promotion.identity` get exported rather than reimplemented.

Cross-cluster app matching reuses the existing rules: name plus namespace,
tolerating environment-suffix conventions, with `apps.match` in the config file
overriding. No new matching mechanism.

## Screen 7: fleet

One app, one row per cluster, environment demoted to a column.

```
checkout                          FLEET

CLUSTER          ENV    VERSION   HEALTH
qa-cluster       qa     v2.14.0   ok 2/2
staging-eks      stg    v2.13.1   ok 3/3
prod-us-east     prod   v2.13.1   ok 6/6
prod-eu-west     prod   v2.12.0   ok 4/4   behind
prod-ap-south    prod   v2.13.1   ok 3/3
team-b           unclas no access          pods list denied
```

An unmatched context renders as `unclassified` and carries prod guardrails, per
`docs/04-multi-cluster.md:70`. Unknown risk stays high risk.

Sorting puts disagreement first, matching the promotion view's default. Nothing
sits ahead of the newest tag, so `behind` is the only relative state the screen
carries. The promotion view keeps `ahead` because it compares against the
environment to its left rather than against a maximum.

## Data flow

Opening the screen states intent to ask every cluster. Under the connection
lifecycle in `docs/04-multi-cluster.md:168` this is the one screen where waking
every context is the point rather than an accident.

1. The server walks every context in the kubeconfig chain, honoring `KUBECONFIG`.
2. Each context connects lazily and resolves the app by the existing identity
   rules.
3. Pod metadata for the digest is fetched on demand and memoized for the
   session, the same mechanism the promotion view uses at
   `docs/04-multi-cluster.md:225`.
4. Each context streams its own answer over the existing websocket. Rows land as
   clusters reply.
5. A cluster behind a VPN never blocks the rest.
6. Idle disconnect resumes when the screen closes.

Two contexts pointing at one cluster merge on `ClusterID` before rendering, per
the failure mode at `docs/04-multi-cluster.md:331`. Without the merge one
cluster appears twice and inflates the behind count, which turns the feature's
headline number into a lie.

That line names two signals, API server URL and cluster UID. Only the first
ships here. `kubeconfig.Context.Server` already holds the URL and costs nothing
to read. No cluster UID exists anywhere in the codebase, and obtaining one means
reading the `kube-system` namespace UID, which needs a permission kubeside does
not currently request and which a namespace-scoped developer would be refused.
The URL alone covers the case the failure mode describes, two kubeconfig entries
aimed at one server. `docs/04-multi-cluster.md:331` gets corrected to say so.

## The promotion matrix fix

`promotion.Env` gains `Contexts []string`. `promotion.Instance` gains `Context`.
The overwrite at `promotion.go:144` becomes a grouping.

Cell behavior for a multi-context environment:

| Contexts in the environment | Cell |
| --- | --- |
| All present and agreeing on tag and digest | Collapses to one version, identical to today |
| Present, disagreeing on tag or digest | `StateSplit`, severe, naming the count, linking to fleet |
| Mixed present and absent | `StateSplit`. Deployed in two of three clusters is not deployed |
| Mixed readable and denied | `StateSplit`. Partial visibility never collapses |
| All denied | `StateDenied`. Not identical to today, see below |
| All absent | `StateAbsent`, identical to today |

The all-denied row needs a correction, found while reviewing the service
wiring on 2026-08-03. Today an environment nobody could read does NOT render
denied. `Service.Promotion` builds its denied placeholders with an empty app
name, the `in.App != ""` filter strips them before `promotion.Build` sees
them, and `resolve()` receives an empty group and returns `StateAbsent` with
the note "not deployed here". Only a banner says otherwise.

The same path also mislabels a partially readable environment: an app absent
from the clusters that answered renders "not deployed here" while an unread
cluster may be running it.

That conflates the two facts this whole feature exists to keep apart, and it
breaks the hard rule against rendering an unknown window as an empty one. It
predates this work, and populating `Env.Unreadable` is what makes it fixable.
Tracked as issue #75 and treated as a ship blocker: shipping with the
information present and ignored is worse than shipping without it.

A split cell never becomes the upstream for the next column. This follows the
existing rule at `promotion.go:151`, where an unreadable environment is not an
agreement and so does not become the comparison basis.

A single-context environment behaves exactly as it does today. That is the
regression guard protecting every closed issue in the promotion feature.

## Error handling

Five row states, none conflated.

| State | Meaning |
| --- | --- |
| `present` | The app is deployed here and readable |
| `absent` | The cluster answered and the app is not there |
| `denied` | RBAC refused the read. Different from absent |
| `unreachable` | The cluster did not answer. Different from absent |
| `pending` | The request is in flight |

`unreachable` is new to the codebase and carries the weight here. A cluster
behind a VPN is not a cluster missing the app, and rendering both blank answers
"is it everywhere" with a guess.

Further cases:

- The app found in no cluster renders the count of clusters asked and their
  states. Never an empty window, per the hard rule in CLAUDE.md. A mistyped app
  name reaches this case immediately.
- The same tag resolving to different digests across clusters is the loudest
  state on the screen, ranked above behind. Two clusters claiming `v2.13.1`
  while running different code is worse than one cluster openly sitting on
  `v2.12.0`.
- A 403 on the namespace list falls through the existing discovery chain and
  names the active mode on the row.
- Expired SSO credentials reuse the inline prompt shipped in #37 rather than
  failing the row.

## Testing

Tests before implementation, per CLAUDE.md.

`internal/fleet`:

- Newest-tag derivation across present placements.
- Behind counting.
- `ClusterID` dedupe when two contexts reach one cluster.
- The five states staying distinct.
- A pending digest never reading as a match.
- Same tag with different digests ranking above behind.

`internal/promotion`:

- A single-context environment unchanged. This test comes first.
- Collapse on full agreement.
- Split on tag disagreement, on digest disagreement, on mixed presence, and on
  partial denial.
- A split cell refusing to serve as upstream for the next column.

Degraded mode, extending the suite from #31: one context unreachable, one
denied, one slow. The screen fills.

Playwright screenshot gate for the new screen, extending the suite from #30.

## The design constraint

CLAUDE.md requires porting from the Claude Design project named `kubeside` and
forbids inventing new UI. That project's `screens/` holds apps, app-detail,
config, config-diff, logs, palette, promotion, and first-run-light. There is no
fleet screen.

The first issue therefore authors `screens/fleet.html` in the design project,
built from `tokens.css` and the row patterns already in `screens/promotion.html`.
Human review of that screen gates the React work. Building the UI first and
backfilling the design would invert the rule the project runs on.

## Issues

Under a new `feature:fleet` label, dependencies before dependents.

| # | Issue | Package |
| --- | --- | --- |
| 1 | Author `screens/fleet.html` in the design project, human-reviewed | design |
| 2 | Export `CompareTags` and identity matching from `promotion` | `internal/promotion` |
| 3 | `internal/fleet`: placement model and derivation | `internal/fleet` |
| 4 | Fleet query across contexts, lazy connect, `ClusterID` dedupe | `internal/fleet`, `internal/api` |
| 5 | Promotion matrix stops collapsing a disagreement | `internal/promotion`, `internal/api`, web |
| 6 | Fleet deltas over the existing websocket | `internal/api` |
| 7 | Screen 7: fleet, ported from issue 1 | web |
| 8 | Palette entry and app-detail entry point | web |
| 9 | Degraded-mode and Playwright coverage | tests |
| 10 | Docs: spec, multi-cluster, and a guide page | docs |

Issue 2 blocks 3 and 5. Issue 3 blocks 4. Issue 1 blocks 7. Issues 2 and 5 touch
`internal/promotion` and stay sequential, per the parallelism rule in CLAUDE.md.

Standalone, outside the label:

| # | Issue |
| --- | --- |
| 11 | Argo CD joins the reference table in `docs/01-problem.md:184` |
| 12 | TUI declined in writing against `docs/05-architecture.md:20` |

Issue 11 answers the first objection a reader raised, and answering it in a
comment thread does not scale. The prose separates GitOps sync state, which is
1:1 with the cluster, from the developer's app view.

Issue 12 records the decline rather than dropping it. `docs/05-architecture.md:20`
already rejects a terminal UI: the four gaps are layout problems a monospace grid
cannot render, and k9s owns the terminal. The decline lands as a closed issue and
a `docs/guide/12-troubleshooting.md` entry, since the question will recur.

## Doc changes this forces

- `docs/03-product-spec.md` commits to four screens. The code already carries
  seven numbered views: apps, app detail, config, diff as 3b, logs, promotion,
  and the palette. Fleet becomes Screen 7 and the spec's count gets reconciled
  with what shipped.
- `docs/04-multi-cluster.md:20` and `:334` promise aggregation with per-context
  expansion inside the promotion view. That promise is replaced by the split
  cell plus the fleet screen, and both lines get rewritten.
- `docs/04-multi-cluster.md:331` claims dedupe by API server URL and cluster
  UID. Only the URL ships. The line gets corrected rather than left aspirational.
- `README.md:5` carries the tagline a commenter read as a shipped promise. It
  stays once the feature ships.

## Done condition

A developer opens kubeside against a kubeconfig holding three prod clusters,
selects one app, and sees which clusters run the newest version and which do
not, with unreachable and denied clusters named rather than blank. The promotion
matrix, on the same data, refuses to show a single version for the environment
holding those three clusters.

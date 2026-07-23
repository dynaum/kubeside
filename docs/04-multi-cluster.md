# Multi-cluster and environments

Developers do not have one cluster. They have qa, stg, and prod, and the question
they ask most often spans all three. Every existing tool treats clusters as a
switcher: pick one, look at it, pick another. That design makes comparison manual
and makes prod accidents likely.

kubeside treats the environment set as the unit of work.

## Model

Three concepts, deliberately separate.

| Concept | Definition |
| --- | --- |
| Context | A kubeconfig context. The connection primitive. |
| Environment | A named tier: `qa`, `stg`, `prod`. Carries a color, a risk level, and a write policy. |
| App | A logical service, present in zero or more environments, matched by name across them. |

One environment maps to one or more contexts. A team running prod across two
regions gets one `prod` environment holding `prod-us-east` and `prod-eu-west`,
and the promotion view aggregates them with per-context detail on expansion.

## Configuration

Zero-config first run, refined by a local file. Never a required setup wizard.

Inference order on first launch:

1. Read every context from the kubeconfig chain, honoring `KUBECONFIG`.
2. Classify each by name against ordered patterns: `prod|production|prd` then
   `stg|staging|stage|uat` then `qa|test|dev|sandbox`.
3. Anything unmatched lands in an `unclassified` environment, treated with prod
   guardrails until the user says otherwise. Unknown risk defaults to high risk.

Persisted to `~/.config/kubeside/config.yaml`:

```yaml
environments:
  - name: qa
    risk: low
    color: green
    write: allow
    contexts: [kind-local, qa-cluster]
  - name: stg
    risk: medium
    color: amber
    write: confirm
    contexts: [staging-eks]
  - name: prod
    risk: high
    color: red
    write: deny          # allow | confirm | deny | break-glass
    contexts: [prod-us-east, prod-eu-west]

apps:
  # Optional. Only needed when an app is named differently per environment.
  checkout:
    match:
      stg: checkout-svc
      prod: checkout

defaults:
  namespaces: []          # empty means discover via SelfSubjectAccessReview
  metrics: auto           # auto | metrics-server | prometheus | none
```

A team commits a shared version to their repo and points `KUBESIDE_CONFIG` at it,
so environment classification and colors stay consistent across the team.

## Connection lifecycle

Naive multi-cluster means three informer sets running permanently, three watch
streams, and three sets of memory. For a developer laptop watching three
environments that does not hold.

Rules:

1. Lazy connect. A context connects on first use, never at startup. Launch
   renders the app list from the last cached snapshot immediately, then
   reconciles.
2. Tiered watching. The active environment gets full informers. Background
   environments get a reduced set: workloads and events only, no pods, no
   configmaps.
3. Idle disconnect. A context with no view referencing it for fifteen minutes
   drops its informers and keeps its cache.
4. Circuit breaker per context. An unreachable cluster fails its own panel and
   never blocks the page. A VPN-only prod cluster is the normal case, not an
   error state.
5. Independent auth. Each context runs its own credential plugin with its own
   token lifecycle. A failure in one shows an inline reconnect action on that
   panel alone.

Every panel carries its own state: `live`, `cached (4m ago)`, `unreachable`, or
`unauthorized`. Stale data is always labeled with its age. Silently showing old
data during an incident is the worst failure this tool could have.

## Screen: promotion view

The headline feature, and the answer to "is the fix in prod yet".

One row per app. One column per environment.

```
                     qa              stg             prod
checkout        v2.14.0  ✓ 2/2   v2.13.1  ✓ 3/3   v2.13.1  ✓ 6/6
payments        v1.8.2   ✓ 2/2   v1.8.2   ✓ 2/2   v1.8.0   ⚠ 5/6
notifications   v0.9.1   ✗ 0/2   v0.9.0   ✓ 2/2   —
```

Each cell holds: image tag, health, ready over desired replicas, and age of the
current revision. Cell states:

- Version behind the environment to its left, rendered as a drift indicator
- Version ahead of the environment to its left, flagged, since prod ahead of stg
  means an out-of-band change
- Absent, rendered as `—`, meaning the app is not deployed there
- Unauthorized, rendered distinctly from absent, since "I cannot see it" and "it
  is not there" are different facts and conflating them is dangerous

Sorting defaults to drift: apps whose environments disagree float to the top.

Version matching uses image tag and digest. Two environments running the same tag
but different digests is a defect worth surfacing loudly, since a mutable tag
means qa and prod are not running the same code.

## Screen: cross-environment config diff

Screen 3 from the product spec, with an environment selector on each side.

```
KEY                    stg                      prod
DATABASE_URL           postgres://stg-db/...    postgres://prod-db/...     expected
LOG_LEVEL              debug                    debug                      suspicious
FEATURE_NEW_CHECKOUT   true                     false                      drift
RETRY_LIMIT            3                        <unset>                    missing
```

Three classifications, computed rather than manual:

- Expected: keys with a per-environment value by design, learned from the pattern
  of differing across every environment pair
- Drift: keys differing where the pattern suggests they should match
- Missing: present in one environment, unset in another

Secrets diff by presence and by hash, never by value. The tool reports "these two
secrets differ" without ever placing prod credentials beside stg credentials on
one screen.

This view answers "works in stg, fails in prod" faster than anything else in the
product.

## Prod safety

A developer holding a prod context in the same switcher as qa will eventually act
on the wrong one. Design against that, rather than trusting attention.

Layers, all active at once:

1. Color. Each environment owns a color, applied to a persistent left border and
   the header. Prod is unmistakable at a glance and at low attention.
2. Name always visible. The environment name renders in the header and in every
   confirmation dialog. Never inferred from memory.
3. Write policy per environment, from the config: `allow`, `confirm`, `deny`, or
   `break-glass`.
4. Typed confirmation. Destructive actions in a `confirm` environment require
   typing the resource name, following the pattern people already know from
   GitHub repository deletion.
5. Break-glass. A prod write in `break-glass` mode unlocks for fifteen minutes,
   requires a stated reason, and writes an entry to the local timeline. P3 gets
   access under pressure without prod being permanently armed.
6. Blast radius preview. Before any write, show what changes, how many pods are
   affected, and the equivalent kubectl command.
7. No bulk actions across environments. Ever. A single control never mutates more
   than one environment.

## RBAC across environments

The normal developer has different permissions in each environment, and the UI
must reflect that per environment rather than globally.

- Permissions resolve per context via SelfSubjectAccessReview, cached for the
  session.
- A control disabled in prod stays enabled in qa, in the same session, on the
  same screen. The promotion view shows an exec button live in qa and disabled in
  prod, with a tooltip naming `pods/exec`.
- Namespace discovery is per context. A developer with three namespaces in qa and
  one in prod sees exactly that.
- No cluster-scoped list is ever a precondition for loading.

## Storage and history per environment

History is per context, since an app in qa and the same app in prod have
unrelated timelines.

SQLite schema partitions on `context_id`. Retention differs by risk: 7 days for
low risk, 30 days for high risk, since prod history matters more and prod changes
less often.

The promotion view reads from cache, which is why launch renders instantly even
with three clusters behind a VPN.

## Failure modes to handle explicitly

| Situation | Behavior |
| --- | --- |
| Prod reachable only over VPN, currently off | Panel shows cached data with an age label and a reconnect action. Other environments unaffected. |
| Credential plugin prompts for SSO in a browser | Inline prompt on the panel. No modal blocking the whole app. |
| Two contexts point at the same cluster | Detected by API server URL and cluster UID, merged with a notice. |
| Context renamed in kubeconfig | Match on cluster UID first, name second, so history survives a rename. |
| Same app name, unrelated services in two environments | Config `apps.match` overrides. A mismatch warning appears when image repositories differ entirely. |
| An environment has 40 contexts | Environment panel paginates and the promotion view aggregates, with per-context detail on expansion. |

## Open questions

1. Does the promotion view need a fourth column for the git SHA, resolved from an
   image label, and where does the mapping come from?
2. Should drift detection fire a notification, or stay passive? A notification
   pulls the product toward alerting, which belongs to a different tool.
3. How much history is worth keeping before SQLite growth becomes a support
   burden on a laptop?
4. In `--serve` mode, does history become shared team state, and if so does that
   turn kubeside into a system of record with backup expectations?
</content>

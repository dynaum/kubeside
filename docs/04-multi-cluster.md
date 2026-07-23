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
| App | A logical service, present in zero or more environments. Identity is namespace plus name within a cluster, matched across environments by the rules below. |

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

Inference runs fresh on every launch and is never written anywhere. A file exists
only if the user creates one, at `~/.config/kubeside/config.yaml`, and kubeside
writes to it only on an explicit save from settings. The "writes nothing to disk"
guarantee covers observed data: no history, no cache, no credentials, no
telemetry. A config file the user chose to create is a different thing.

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
  # Optional. Needed when name or namespace differs per environment, or when
  # the same name exists in two namespaces of one cluster.
  checkout:
    match:
      qa: team-a-qa/checkout
      stg: team-a/checkout-svc
      prod: prod/checkout

defaults:
  namespaces: []          # empty means probe: try a namespace list, else the
                          # context's own namespace, else require this list
  metrics: auto           # auto | metrics-server | prometheus | none
```

Cross-environment matching without config uses name plus namespace, tolerating
environment-suffix conventions (`team-a-qa` matches `team-a-prod`). Two
unrelated services sharing a name in different namespaces of one cluster stay
two rows, never merged. When matching is ambiguous the promotion view says so
on the row instead of guessing.

A team commits a shared version to their repo and points `KUBESIDE_CONFIG` at it,
so environment classification and colors stay consistent across the team.

One catch a shared file must survive: context names are personal. One
teammate's `prod-us-east` is another's `arn:aws:eks:us-east-1:...`. A shared
config therefore identifies clusters by API server URL or cluster name:

```yaml
  - name: prod
    risk: high
    write: deny
    clusters:
      - https://A1B2C3.gr7.us-east-1.eks.amazonaws.com
```

`contexts:` stays available for personal configs. When both appear, cluster
matching wins, since it survives every rename and every naming convention.

## Connection lifecycle

Naive multi-cluster means three informer sets running permanently, three watch
streams, and three sets of memory. For a developer laptop watching three
environments that does not hold.

Rules:

1. Lazy connect. A context connects on first use, never all at once at startup.
   The environment matching `current-context` connects first, so the developer's
   usual workspace renders while the others are still handshaking.
2. Tiered watching. The active environment gets full informers. Background
   environments get a reduced set: workloads and events only, no pods, no
   configmaps.
3. Idle disconnect. A context with no view referencing it for fifteen minutes
   drops its informers and retains its last in-memory snapshot, which then
   renders with an age label until the context reconnects.
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

Digests need a note on mechanism, because they live in pod status (`imageID`),
not in workload specs, and background environments deliberately watch no pods.
While the promotion view is open, kubeside fetches pod metadata on demand for
the compared apps, memoized for the session like timeline reconstruction.
Digest cells render a pending state until the fetch lands, and tag comparison
never waits for it.

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
   requires a stated reason, and writes an entry to the session timeline. Marina gets
   access under pressure without prod being permanently armed.
6. Blast radius preview. Before any write, show what changes, how many pods are
   affected, and the equivalent kubectl command.
7. No bulk actions across environments. Ever. A single control never mutates more
   than one environment.

One boundary stated plainly: these layers protect against accidents, not
intent. The write policy lives in a config file the user controls and is
trivially editable. RBAC is the security boundary. kubeside's guardrails are
ergonomics on top of it, never a substitute for it, and the docs will say so
wherever the policies are described.

## RBAC across environments

The normal developer has different permissions in each environment, and the UI
must reflect that per environment rather than globally.

- Permissions resolve per context via SelfSubjectAccessReview, cached for the
  session.
- A control disabled in prod stays enabled in qa, in the same session, on the
  same screen. The promotion view shows an exec button live in qa and disabled in
  prod, with a tooltip naming `pods/exec`.
- Namespace discovery is per context, and a 403 on the namespace list is a
  normal answer, not an error. The chain: probe one list, fall back to the
  context's `namespace` field, then to the configured list, with the active
  mode named in the UI. A developer with three namespaces in qa and one in
  prod sees exactly that.
- A refused cluster-scoped list never blocks loading.

## History per environment

kubeside stores nothing on disk. History comes from the cluster itself,
reconstructed on demand, plus whatever the session observes while running. The
mechanism is in [05-architecture.md](05-architecture.md).

Two consequences specific to multi-cluster:

1. History is per context by construction, since each cluster holds its own
   ReplicaSets, ControllerRevisions, and Helm release secrets. No partitioning
   logic is needed, because the data was never merged.
2. Reconstruction depth differs per environment, and for a useful reason. Prod
   changes less often than qa, so the same `revisionHistoryLimit` of 10 reaches
   back weeks in prod and perhaps a day in qa. The timeline labels the reach it
   achieved rather than assuming a fixed window.

Launch has no cache to read from, so the promotion view fills in per environment
as each connection completes. Each cell carries its own loading state and a
cluster behind a VPN never blocks the rest of the grid.

## Failure modes to handle explicitly

| Situation | Behavior |
| --- | --- |
| Prod reachable only over VPN, currently off | Two states, never conflated. Connected earlier this session: in-memory snapshot with its age. Never connected this session: "nothing known yet" with a reconnect action. Other environments unaffected either way. |
| Credential plugin prompts for SSO in a browser | Inline prompt on the panel. No modal blocking the whole app. |
| Two contexts point at the same cluster | Detected by API server URL and cluster UID, merged with a notice. |
| Context renamed in kubeconfig | Match on cluster UID first, name second, so history survives a rename. |
| Same app name, unrelated services in two environments | Config `apps.match` overrides. A mismatch warning appears when image repositories differ entirely. |
| An environment has 40 contexts | Environment panel paginates and the promotion view aggregates, with per-context detail on expansion. |

## Resolved questions

3. How much history to keep on disk. Resolved 2026-07-22: none. kubeside writes
   nothing. History is reconstructed from the cluster and buffered in memory for
   the session only.
4. Whether `--serve` mode history becomes shared team state. Resolved by the
   same decision. There is no stored history to share, so kubeside never becomes
   a system of record and never inherits backup, retention, or audit
   obligations.

## Open questions

1. Does the promotion view need a fourth column for the git SHA, resolved from an
   image label, and where does the mapping come from? Leaning toward reading
   `org.opencontainers.image.revision` opportunistically, showing the column only
   when populated, and requiring no registry credentials in v1.
2. Should drift detection fire a notification, or stay passive? Leaning passive.
   A notification pulls the product toward alerting, which is a different tool
   with a much harder reliability promise.
3. In `--serve` mode the session is the server lifetime, so the in-memory
   buffer quietly accumulates months of observations a team will learn to
   depend on, then lose on the next pod restart, during exactly the incident
   where they reached for it. Leaning toward labeling the horizon marker with
   the server start time plus an explicit ephemerality warning, and possibly
   capping the displayed window so nobody builds a habit on it.
</content>

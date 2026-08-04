# Fleet

Is every cluster running the latest version? One app, one row per cluster.

Promotion compares environments side by side. Fleet answers a question
promotion cannot phrase: a team running prod across two regions, or any
environment holding more than one cluster, needs to know whether those
clusters agree with each other, not only whether prod agrees with stg.

## Opening it

Three ways in:

- The command palette (`cmd+k`), from any app: "*app* across every cluster".
- The "Fleet" button on the app detail screen.
- Directly, at `#fleet/<app>/<namespace>`.
- From the promotion screen, click a `split` cell. A split cell means the
  clusters inside that environment disagree, and fleet is where you see which
  ones.

Opening this screen wakes every context in your kubeconfig, not only the one
you were looking at. No other screen does this. The app list and app detail
connect lazily, one context at a time, as you look at them. Fleet asks all of
them at once, on open, because "every cluster" is the question it exists to
answer.

## Reading the table

| State | Means |
| --- | --- |
| `present` | The cluster runs the app. |
| `absent` | The cluster answered, and does not run it. A schedule, not a problem: the app just is not deployed there yet. |
| `denied` | The cluster refused the read. The note names the RBAC rule that would fix it, for example "cannot read Deployment here; the role needs: list deployments in team-a". |
| `unreachable` | The cluster did not answer at all. VPN, DNS, or the cluster is down. |
| `pending` | Still asking. |

The distinction that matters most is `unreachable` against `absent`. A prod
cluster reachable only over VPN, currently off, is `unreachable`: kubeside
never saw it this time, so it never claims the app is missing there. Only a
cluster that actually answered and does not run the app gets `absent`.
Conflating the two, "I could not look" read as "it is not there", is the
mistake this screen exists to prevent, and it is the reason opening fleet
wakes every context instead of trusting whatever was last connected.

A present row can also carry:

- **behind** — an older version than the newest tag another cluster in the
  fleet runs.
- **mutable tag** — this cluster's tag also resolves to a different digest
  somewhere else in the fleet. Two clusters agree on the version number and
  are not running the same code. This outranks behind: a cluster openly a
  version behind is a schedule, two clusters claiming agreement while running
  different builds is a defect.

Rows sort by how much the cluster told you, worst first: mutable tag, then
unreachable, then behind, then denied, then present with no comparable
version, then pending. Absent sits with the healthy rows at the bottom,
because "not deployed here" is not a disagreement.

## Version unknown

An image pinned by digest (`myapp@sha256:...`) carries no tag. There is
nothing to compare it against, so the row reads "version unknown: the image
is pinned by digest" rather than folding it into the newest tag or treating
it as a match. Pinning by digest is not a defect; reading it as agreement
would be.

## Clusters that are really one cluster

A kubeconfig commonly holds one cluster under two context names, often
because one of them carries credentials that stopped working. Fleet detects
this by API server URL and merges the two into one row, so the same cluster
is never counted twice. The row names what it merged: "also
prod-us-east-arn" beside the context it kept. The row it keeps is whichever
context answered with the most, so a stale duplicate never hides a live one.

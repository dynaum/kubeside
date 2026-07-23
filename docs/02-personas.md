# Personas

Five people kubeside serves, one stakeholder who installs it, and three
explicitly out of scope. Every feature decision resolves against this list.

## Summary

| # | Persona | Priority | Kubernetes literacy | Typical RBAC in prod |
| --- | --- | --- | --- | --- |
| P1 | Ana, application developer | Primary | Medium | Namespace read, no exec |
| P2 | Rafael, tech lead | Primary | High | Namespace read + exec |
| P3 | Marina, developer on call | Primary (mode) | Medium to high | Namespace read + exec, scoped write |
| P4 | Bruno, QA engineer | Secondary | Low | Namespace read in qa and stg only |
| P5 | Clara, new joiner | Secondary | Low | Namespace read in qa |
| S1 | Diego, platform engineer | Stakeholder, not a user | Expert | Cluster admin |

P3 is a state P1 and P2 enter, not a separate headcount. Treated separately
because the interface requirements change sharply under incident pressure.

---

## P1. Ana, application developer (primary)

Ana owns two or three backend services. She writes the Dockerfile and the Helm
values, and a pipeline deploys for her. Kubernetes is infrastructure she passes
through, not a system she studies.

Context

- Works in an IDE and a terminal all day.
- Deploys through CI, rarely by hand.
- Holds cluster-admin on a local kind cluster and in qa. Namespace-scoped read in
  stg and prod, usually without exec.
- Learned enough kubectl to run `logs`, `describe`, and `get pods`. Reaches for a
  GUI beyond that.

Her daily questions

1. Did my deploy land, and is the new pod healthy?
2. Why is the pod restarting?
3. What is in the logs, across all replicas, without opening five terminals?
4. Which environment variable did the container actually get, and where did the
   value come from?
5. Is the failure mine or the platform's?

What she uses today and why it fails

Freelens or Lens, because the sidebar is browsable without memorizing commands.
Fails on: logs are per-pod, so reading a three-replica deployment means three
tabs. Resolved configuration requires cross-referencing a Deployment spec against
two ConfigMaps and a Secret by hand. Metrics frequently render wrong, which
teaches her to distrust the numbers.

What kubeside gives her

The app list keyed by workload owner, not resource kind. One log stream per
workload. One resolved-config table with provenance per row. A timeline that
answers "what changed" without asking her to reconstruct history from events that
expired an hour ago.

Success metric

Time from "my deploy looks broken" to a specific cause, under 60 seconds, without
leaving the app.

---

## P2. Rafael, tech lead (primary)

Rafael reviews what ships, unblocks four developers, and owns the release
conversation with the product side. High Kubernetes literacy and fluent in
kubectl. He uses kubeside for the questions kubectl answers badly.

Context

- Runs k9s already and likes it. He will not abandon a terminal for routine work.
- Reads across services and across environments constantly.
- Gets asked "is that fix in prod yet?" in Slack several times a week, and has to
  go find out.

His daily questions

1. Which image tag is running in qa, stg, and prod right now?
2. What differs in configuration between stg and prod for this service?
3. Did anyone change something in prod outside the pipeline?
4. Is this service ready to promote?
5. Which of my team's services are unhealthy at this moment?

What he uses today and why it fails

kubectl plus k9s plus a browser tab per cluster. Comparing three environments
means three contexts and manual diffing of YAML. No tool holds all three at once
in a way that makes a difference visible.

What kubeside gives him

The promotion view: one row per app, one column per environment, image tag and
health in each cell. Cross-environment config diff. A timeline showing every
change with its actor, so an out-of-band `kubectl edit` in prod becomes visible
instead of forensic.

Success metric

Answers "is the fix in prod" in one glance, no context switching.

---

## P3. Marina, developer on call (primary, as a mode)

Marina is P1 or P2 at 03:00 with a pager alert. Her cognitive capacity is low,
the stakes are high, and the cost of a wrong click is enormous.

Context

- Woken up. Reading on a laptop, sometimes on a phone.
- Under time pressure with someone watching a status page.
- Holds more write permission in prod than usual, through a break-glass role.

What she needs

1. What changed in the last hour, ranked by relevance, across the whole app.
2. Logs from every replica, merged and time-ordered, filtered to errors.
3. Correlation: the restart, the config change, and the error spike on one axis.
4. Strong protection against destructive mistakes in prod.
5. A shareable link showing a colleague the same state.

What she uses today and why it fails

Grafana for the metric, a terminal for logs, and the cloud console for events.
Three tools, three time pickers, no shared axis. Kubernetes events are gone after
an hour by default, so the most valuable evidence expires precisely when she
needs it.

What kubeside gives her

Persistent event and change history in local storage, so the timeline survives
the Kubernetes event TTL. An incident view scoped to a time window. Prod
guardrails: distinct color, read-only by default, typed-name confirmation for
anything destructive, and a persistent banner naming the environment.

Success metric

The "what changed" answer arrives without typing a query, and no accidental
destructive action is possible without deliberate effort.

---

## P4. Bruno, QA engineer (secondary)

Bruno verifies builds in qa and stg. Low Kubernetes literacy by design, since
infrastructure is not his job.

Context

- Read-only access to qa and stg. No prod access at all.
- Needs to know whether the environment is healthy before filing a bug.
- Files reports developers reject as "environment issue, not a bug", which wastes
  both sides.

What he needs

1. Which build is deployed in this environment right now?
2. Is the environment healthy, or am I testing a broken deploy?
3. Logs at the moment of a failure, copyable into a bug report.
4. Zero risk of breaking anything by clicking.

What kubeside gives him

The promotion view answers the build question without asking a developer. Health
at a glance per environment. A log permalink to paste into a ticket. Read-only
enforcement derived from real RBAC, with disabled controls rather than hidden
ones, so he learns what exists.

Success metric

Bug reports arrive with the build identifier and the relevant log already
attached.

---

## P5. Clara, new joiner (secondary)

Week one. Clara knows the language and the codebase conventions, has never
operated this cluster, and does not yet know how the services relate.

What she needs

1. A mental model of which services exist and how they connect.
2. Discoverability, since she does not know the vocabulary well enough to search.
3. Safety, since she is the most likely person to run a destructive command by
   accident.
4. Learning transfer: understanding what the UI does underneath.

What kubeside gives her

Application grouping teaches the service topology directly. Disabled controls
with an RBAC explanation teach what exists and what she lacks. Every view exposes
the equivalent kubectl command, so the tool builds fluency instead of replacing
it. Two Hacker News threads independently raised the "GUIs made me lose my
kubectl fluency" complaint, which is worth designing against.

Success metric

Productive without a colleague sitting beside her, and more fluent in kubectl
after a month, not less.

---

## S1. Diego, platform engineer (stakeholder, not the user)

Diego installs kubeside, sets the RBAC, and runs `--serve` mode for the team. He
personally uses k9s and Headlamp and has no intention of switching.

Why he matters anyway

- He approves or blocks adoption.
- He configures environments, metrics sources, and access.
- He absorbs the support load when the tool confuses a developer.

What he needs from kubeside

1. A single binary with no cluster-side agent required.
2. Read-only by default, with permissions derived from the user's own kubeconfig,
   never a shared service account.
3. Low apiserver load. Informers and a shared watch cache, not polling loops.
4. Honest behavior under namespace-scoped RBAC, since that is what his developers
   get.
5. Deployment through Helm for the shared mode.

Design consequence

kubeside never asks for cluster-admin. Anything requiring cluster scope degrades
to namespace scope and says so.

---

## Anti-personas

Stated explicitly so scope creep has something to fail against.

### A1. Cluster operator

Nodes, capacity planning, etcd health, control plane upgrades, PersistentVolumes,
StorageClasses. k9s and Headlamp serve this well. kubeside does not compete here
and will not grow a node view.

### A2. Security and compliance auditor

RBAC visualization, policy enforcement, CVE scanning, admission control, audit
export. A different product with different data retention and different buyers.

### A3. FinOps and cost analyst

Cost allocation, rightsizing, chargeback, commitment planning. OpenCost and
Kubecost own the space and integrate elsewhere.

Adding any of the three converts kubeside into the fifth indistinguishable
general-purpose dashboard, which is the exact failure identified in
[01-problem.md](01-problem.md).

---

## Access model implications

Personas differ mostly in permissions, so the UI must treat RBAC as a first-class
input rather than an error condition.

| Access pattern | Who | Requirement |
| --- | --- | --- |
| Cluster-wide read | Diego, Rafael in qa | Full app discovery across namespaces |
| Namespace-scoped read | Ana, Bruno, Clara, most prod access | App discovery falls back to a configured namespace list. No cluster-scope list calls at startup. |
| No exec | Ana and Bruno in prod | Exec control renders disabled, tooltip naming `pods/exec` |
| Break-glass write | Marina during an incident | Write controls unlock, every action logged to the local timeline |

Three rules follow:

1. Never issue a cluster-scoped list as a precondition for the app to load.
   Discover through the user's own SelfSubjectAccessReview and fall back to
   configured namespaces.
2. Never hide a control the user lacks permission for. Disable, and name the
   missing verb. This is the top complaint in Headlamp's launch thread and stands
   unaddressed six years later.
3. Never require a shared service account in local mode. Permissions belong to
   the user, always.

---

## Jobs to be done

Ranked by frequency across P1 to P5, which sets build order.

1. Tell me whether my app is healthy right now. (all)
2. Show me logs for the whole workload, merged. (Ana, Marina, Bruno)
3. Tell me what changed and when. (Rafael, Marina)
4. Show me the configuration the container actually received. (Ana, Marina)
5. Tell me which version is in each environment. (Rafael, Bruno)
6. Show me how two environments differ. (Rafael, Marina)
7. Teach me the shape of the system. (Clara)
8. Stop me from breaking prod. (Marina, and every persona by accident)
</content>

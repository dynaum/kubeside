# kubeside: execution rules

## Goal

Ship kubeside to v1.0: a Kubernetes client scoped to the application developer,
answering four questions across qa, stg, and prod. Single Go binary serving an
embedded React UI. No storage layer.

The design docs in `docs/` are the specification. They have been reviewed and
corrected once already. Treat them as authoritative and update them when
implementation contradicts them, rather than silently diverging.

The visual system lives in the Claude Design project named `kubeside`, including
`tokens.css` and every screen. Port from there; do not invent new UI.

## Order of work: feature by feature

Work one feature at a time, top to bottom, and finish it end to end before
starting the next. A feature is a vertical slice: backend, transport, UI, and
tests, shipped together and demonstrable by a human.

Do not work by layer. Building every backend interface first, then every screen,
produces months of work with nothing a developer can open. Each feature below
should leave the product measurably more useful than it was that morning.

Milestones still exist on the issues for reference, but they no longer drive
order. The `feature:*` label does.

| # | Label | Delivers |
| --- | --- | --- |
| 1 | `feature:connect` | Reads your kubeconfig, connects every context, prints an app list you recognize |
| 2 | `feature:apps` | The app list in a browser, multi-environment, with health and metrics |
| 3 | `feature:logs` | Logs for a whole workload, merged, with availability edges marked |
| 4 | `feature:timeline` | What changed and when, reconstructed from the cluster |
| 5 | `feature:config` | The configuration the container actually received |
| 6 | `feature:promotion` | Which version is in qa, stg, and prod, side by side |
| 7 | `feature:diff` | How two environments differ |
| 8 | `feature:forward` | Port-forward, so the loop ends where the developer needs it |
| 9 | `feature:palette` | Every action reachable from the keyboard |
| 10 | `feature:writes` | Prod guardrails, before any destructive action exists |
| 11 | `feature:exec` | Shell into a container |
| 12 | `feature:release` | Windows, visual regression, degraded-mode tests, docs, launch |

Ordering follows the jobs-to-be-done ranking in `docs/02-personas.md`, which is
sorted by how often each job comes up across the personas. Health first, then
logs, then history, then config. Promotion sits at position 6 despite being the
headline differentiator, because Rafael asks it a few times a week while Ana
reads logs a few times an hour.

`feature:writes` deliberately precedes `feature:exec`: guardrails must exist
before the first genuinely destructive capability does, not after.

Within a feature, dependencies before dependents. A feature is done when a human
can open the product and use the thing it names.

## Definition of done, per issue

1. Tests written before implementation. The grouping engine and degraded-mode
   paths are the highest-value tests in the project.
2. `go build ./...` and `go test ./...` pass.
3. The issue's stated done-condition is demonstrably met, with the command and
   its output shown.
4. Docs updated if behavior diverged from `docs/`.
5. One commit per issue, message ending with `Closes #N`.
6. Pushed to `main`.

## Hard rules

- No commit trailers of any kind. No `Co-Authored-By`, no `Generated with`.
- Never claim something passes without running it and showing the output.
- Never write to disk at runtime. No database, no cache file. This is the
  product's central bet, recorded in `docs/04-multi-cluster.md`.
- Never hide a control for lack of permission. Disable it and name the verb.
- Never render an unknown window as an empty one.
- Never add a feature belonging to an anti-persona in `docs/02-personas.md`.
  Decline the request in the issue with a link.

## When to stop and ask

Stop, post a comment on the issue, and apply the `blocked-on-human` label when:

- An issue carries `blocked-on-human` already.
- A decision would change the product thesis rather than implement it.
- An open question in `docs/04-multi-cluster.md` blocks progress.
- Tests fail for a reason that suggests the spec is wrong, not the code.

Do not guess past a blocker to keep the loop moving. A stalled loop that
reports honestly is worth more than a running loop producing work built on a
wrong assumption.

## The kill criterion

Issue #5, the last of `feature:connect`, can end this project. If the grouping
engine does not produce an app list a developer recognizes as their own apps,
the thesis is wrong and work stops. That judgment is human and requires a real,
organically grown cluster; a kind cluster with hand-written demo manifests
proves nothing because its labels would be perfect by construction.

Do not start `feature:apps` before #5 is resolved by a human.

## Parallelism

Issues within the current feature that touch disjoint packages may be worked
concurrently by subagents. Issues touching the same package must be sequential.
The grouping engine (#3) is a dependency of nearly everything downstream and is
never parallelized with work that consumes it.

Do not parallelize across features. Two features in flight means neither is
finishable, which defeats the point of slicing vertically.

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

## Order of work

Work the lowest-numbered open milestone first: M0, then M1 through M5. Within a
milestone, dependencies before dependents. `docs/06-roadmap.md` holds the
reasoning behind the ordering.

Never start a milestone before the previous one's exit criteria are met.

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

## The M0 kill criterion

Issue #5 can end this project. If the grouping engine does not produce an app
list a developer recognizes as their own apps, the thesis is wrong and work
stops. That judgment is human and requires a real, organically grown cluster; a
kind cluster with hand-written demo manifests proves nothing because its labels
would be perfect by construction.

Do not implement M1 before #5 is resolved by a human.

## Parallelism

Issues within a milestone that touch disjoint packages may be worked
concurrently by subagents. Issues touching the same package must be sequential.
The grouping engine (#3) is a dependency of most of M1 and is never parallelized
with work that consumes it.

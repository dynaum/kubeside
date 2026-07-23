# kubeside

A Kubernetes client scoped to the developer, not the cluster operator.

Single Go binary. Launch from the terminal, renders in the browser. Answers four
questions and deliberately refuses to answer anything else:

1. Is my app up?
2. What changed, and when?
3. What do the logs say, across every pod at once?
4. What configuration did the container actually receive?

Across qa, stg, and prod side by side.

## Status

Design stage. No code yet. Documentation first.

## Why another one

The existing tools are excellent at what they do and all solve the operator's
problem. They mirror the Kubernetes API tree: pick a resource kind, browse
instances. A developer thinks in services, not ReplicaSets, and needs history
more than a live snapshot.

See [docs/01-problem.md](docs/01-problem.md) for the evidence behind this claim,
pulled from issue trackers in July 2026.

## Planned shape

```
kubeside            # local mode: starts server, opens http://localhost:7654
kubeside --serve    # in-cluster mode: same binary, team web UI
```

One artifact serves the local dev tool and the shared team dashboard. No rewrite
between the two.

No setup step. kubeside reads the kubeconfig already on the machine, loads every
context, and runs exec credential plugins natively. If `kubectl` works, kubeside
works. The file is read-only input: kubeside never writes to it, never copies
credentials, and never sends anything to a remote service.

## Documentation

| Doc | Contents |
| --- | --- |
| [01-problem.md](docs/01-problem.md) | Landscape research, complaint clusters, the four uncovered gaps |
| [02-personas.md](docs/02-personas.md) | Five users, one stakeholder, three anti-personas, access model |
| [03-product-spec.md](docs/03-product-spec.md) | Principles, the four screens, non-goals, anti-requirements |
| [04-multi-cluster.md](docs/04-multi-cluster.md) | Environments, promotion view, cross-env config diff, prod guardrails |
| [05-architecture.md](docs/05-architecture.md) | Stack, data flow, storage, auth, performance budget |
| [06-roadmap.md](docs/06-roadmap.md) | Milestones and what ships in each |

## License

Apache-2.0.
</content>
</invoke>

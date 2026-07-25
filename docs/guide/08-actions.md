# Port-forward, exec, and the write guardrails

Reading is the product. These are the three things that write, and each one is
gated twice: by your cluster's RBAC, and by the environment's write policy.

## Port-forward

Forward a port from a pod of the app to your machine. kubeside binds
`127.0.0.1` only, never `0.0.0.0`: a forwarded prod database should not become
reachable from the coffee-shop network.

Forwards are deduplicated by target, listed while they run, and closed when you
close them or when the process exits. Nothing survives the process.

Needs `create` on `pods/portforward` in that namespace.

## Exec

A shell in a container, proxied through the kubeside process. Keystrokes cross
the local websocket and then a SPDY stream; the browser never talks to an
apiserver and credentials never leave the process.

The default command probes for bash and falls back to sh in one invocation:

```sh
/bin/sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'
```

The probe has to come before the exec. A failed `exec` replaces the shell with
nothing and exits 127, so a fallback placed after one never runs, which is
exactly how the first version of this failed on an alpine image.

Needs `create` on `pods/exec` in that namespace.

## The write policy

Every environment carries one, inferred from its name or set in
[the config file](10-config-file.html):

| Policy | What happens |
| --- | --- |
| `allow` | The action proceeds. |
| `confirm` | You type the resource's name to confirm. |
| `break-glass` | The environment is locked. You unlock it with a stated reason, which arms it for 15 minutes, and the typed name is still required. |
| `deny` | The action does not proceed, and the refusal names the policy and where to change it. |

Unlocking records who unlocked, why, and until when, on the timeline. A closed
window says whether it expired or was never opened, because those lead to
different next actions.

## Blast radius

Before a destructive action, the confirmation states what it will touch: how
many pods, and which. When that cannot be determined, it says the blast radius
is unknown rather than showing a reassuring zero.

## RBAC is the boundary, guardrails are ergonomics

The guardrails above are about ceremony: they slow a human down in an
environment that deserves it. They are not security. The security boundary is
your cluster's RBAC, resolved with SelfSubjectAccessReview and never cached when
the check itself fails.

Both answers always travel together, so a refusal tells you which one refused.

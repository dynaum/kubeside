# Resolved configuration

The fourth question is what configuration the container actually received. Not
the template, not the ConfigMap, but the values the running process was handed.

![Resolved configuration](../images/config.png)

## What is resolved

Every source a container's environment can come from, merged and attributed:

- `env` inline values
- `envFrom` on ConfigMaps and Secrets
- `valueFrom` `configMapKeyRef` and `secretKeyRef`
- downward API values (`status.podIP`, `metadata.name`, resource limits)
- mounted ConfigMap and Secret volumes

The `SOURCE` column names where each key came from. Reading `LOG_LEVEL: debug`
without knowing it came from a ConfigMap called `payments-cfg` is half an
answer.

## Secrets

Secret values are masked because kubeside never fetched them. Masking a value
already in the browser would be a rendering decision; not reading it is a
property.

`Reveal` asks the cluster whether you may `get` that specific Secret, using a
SelfSubjectAccessReview. If the answer is no, the control is disabled and names
the verb:

```
Reveal   needs get on secrets/payments-db in team-a
```

Never hidden. A control that vanishes teaches you nothing; a disabled one that
names its verb tells you exactly what to ask your platform team for.

Every reveal is recorded on the timeline before the value is returned, so a
value that reached a screen is always a value the timeline knows about.

## Compared with the previous revision

The previous revision's pod template is still on the cluster, inside its
ReplicaSet, which is the only place old configuration survives at all. Each key
is compared against it:

| State | Means |
| --- | --- |
| `unchanged` | Same as the previous revision. |
| `was X` | Changed, with the old value shown. |
| `added` / `removed` | The key appeared or disappeared. |
| `source-changed` | Same value, different source. |
| `not recoverable` | The old value cannot be known: it came from a ConfigMap or Secret whose contents have since changed. |
| `runtime` | Assigned at runtime, like `POD_IP`. Comparing it would be meaningless. |

`not recoverable` is the honest answer for most Secret and `envFrom` keys, and
it is deliberately not dressed up as `unchanged`.

## Across environments

The same screen compares one app in two environments. See
[promotion and drift](07-promotion.html).

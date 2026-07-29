# Promotion and drift

Is the fix in prod yet? One row per app, one column per environment.

![The promotion matrix](../images/promotion.png)

## Reading the matrix

| Cell | Means |
| --- | --- |
| version, plain | Same as the environment to its left. |
| version, marked `behind` | An older version than upstream. Normal mid-promotion. |
| version, marked `ahead of upstream` | A version prod has that stg does not. Usually a hotfix nobody back-merged, and the worst thing on this screen. |
| `same tag, different digest` | The tag matches but the image does not. A mutable tag was rebuilt. |
| `absent` | The app does not exist in that environment. |
| `not readable` | Your role cannot read that environment. Not the same as absent. |

Rows sort worst first, so `ahead of upstream` floats to the top.

Version ordering only claims an order when the tags support one. `v1.8.0` and
`v1.7.4` compare; `sha-abc123` and `sha-def456` do not, and the cell says
`differs` rather than inventing a direction.

## Cross-environment configuration diff

Comparing two environments key by key, each difference is classified rather than
listed flat:

| Class | Means |
| --- | --- |
| `match` | Identical. |
| `expected` | Different by definition: a URL containing the environment name, a pod IP, a replica count. |
| `drift` | Different with no reason kubeside can see. This is the column to read. |
| `suspicious` | Different in a way that usually signals a mistake, such as `LOG_LEVEL: debug` in prod. |
| `missing` | Present in one environment, absent in the other. |

Secret values never reach the browser and never reach a screen. The comparison
is a fingerprint: kubeside reads both values inside its own process, keyed-hashes
each one, compares the fingerprints, and discards the values. You learn whether
two environments hold the same credential without either being rendered
anywhere.

Two details worth stating plainly, because the shorter sentence would be a lie.
The read happens: this is the one feature that fetches Secret contents, and it
only runs after your cluster answers that you may `get` that Secret. And the
fingerprint is keyed with a random key generated when the process starts, so a
digest cannot be checked against a guessed value, by you or by anyone who sees
your screen.

## Matching apps across environments

By default an app is matched by name plus namespace, tolerating environment
suffixes: `team-a-qa/checkout` matches `team-a-prod/checkout`. Two unrelated
services that share a name in different namespaces stay two rows.

When the name or namespace genuinely differs per environment, say so in
[the config file](10-config-file.html). When matching is ambiguous, the row says
so instead of guessing.

# The config file

Optional. kubeside infers everything on a first run and writes nothing. A file
exists only because you made one.

## Where it is read from

In order: `--config`, then `$KUBESIDE_CONFIG`, then
`$XDG_CONFIG_HOME/kubeside/config.yaml`, then `~/.config/kubeside/config.yaml`.

Looking for it creates nothing, not the file and not the directory. No file at
the end of that list is a normal first run. A file you named explicitly that
does not exist is an error, because you asked for that one.

## Parsing is strict

Every rejection names the offending value, and an unknown key fails the load
rather than being ignored. A misspelled `environmets:` that quietly did nothing
would leave you believing prod is guarded when it is not.

## The whole schema

```yaml
environments:
  - name: qa
    risk: low               # low | medium | high
    color: green
    write: allow            # allow | confirm | deny | break-glass
    contexts: [kind-local, qa-cluster]

  - name: prod
    risk: high
    color: red
    write: break-glass
    # A shared file identifies clusters by API server URL, because context
    # names are personal: one teammate's prod-us-east is another's EKS ARN.
    clusters:
      - https://A1B2C3.gr7.us-east-1.eks.amazonaws.com

apps:
  # Only needed when the name or namespace differs per environment.
  checkout:
    match:
      qa: team-a-qa/checkout
      stg: team-a/checkout-svc
      prod: prod/checkout

defaults:
  namespaces: []            # empty means probe: try listing, else the context's
                            # own namespace
  metrics: auto             # auto | metrics-server | prometheus | none

namespaceAxis:
  # When one cluster holds several environments distinguished by a namespace
  # prefix. Anchor one segment so platform namespaces are not misread as
  # tenant services.
  pattern: "{env}-{system}-{service}"
  systems: [acme]
```

## Rules worth knowing

Fields left out inherit the classification of the environment's own name, so an
entry that only binds contexts keeps the guardrails its name implies. Setting
`risk` without `write` re-derives the write policy from the new risk, so raising
an environment's risk also tightens it.

A context the file says nothing about still classifies by name. Configuring one
environment never blinds kubeside to the rest.

When both `contexts:` and `clusters:` appear, cluster matching wins: it survives
every rename and every naming convention.

## Sharing one across a team

Commit it and point everybody at it:

```
export KUBESIDE_CONFIG=~/work/platform/kubeside.yaml
```

Use `clusters:` rather than `contexts:` in a shared file, for the reason above.

# Launch

Everything here needs a human. The site is built and the release pipeline is
wired, but publishing a first release under someone's name, and posting to three
communities as that person, are their decisions to make and their words to send.
Drafts below; nothing has been posted.

## 1. The site

Pages is enabled with GitHub Actions as its source, and `PAGES_ENABLED` is set,
so every push to `main` that touches the docs rebuilds and republishes
`https://dynaum.com/kubeside/`.

To publish without waiting for a push:

```
gh workflow run pages.yml
gh run watch
```

Preview it locally with:

```
npm --prefix web run site && python3 -m http.server -d web/site-dist 7788
```

The site is generated from `docs/` and from the screenshots the visual gate
already compares against, so it cannot drift from the product without failing a
test. `npm --prefix web run test` checks that every document became a page and
that no internal link points at nothing.

## 2. Cut the release

Nothing publishes without a tag, and the GoReleaser release is created as a
draft, so tagging is safe and reversible up to the moment you press publish.

```
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
gh run watch                       # the release workflow builds the UI first
gh release view v1.0.0             # a draft: read the notes before publishing
```

Publishing pushes a formula to `dynaum/homebrew-tap`, which is what makes
`brew install dynaum/tap/kubeside` in the README true. Until then that line is
marked as pending, and the README says so.

Before publishing, run the binary once from the artifact on a machine that is
not this one. The release workflow verifies the UI reached the binary; it does
not verify that a downloaded archive opens on a Mac with Gatekeeper.

## 3. Post

Three audiences, three registers. Each is a draft to edit, not to send as-is.
The rule for all of them: claim only what the product does today. Every sentence
below is checkable against the repository.

### Show HN

Title:

> Show HN: Kubeside – a Kubernetes client scoped to the developer, not the operator

Body:

> I kept opening k9s to answer four questions and having to think in ReplicaSets
> to get to them: is my app up, what changed and when, what do the logs say
> across every pod, and what configuration did the container actually receive.
> Every tool I tried is excellent at the operator's job and treats clusters as a
> switcher: pick one, look at it, pick another. Developers have qa, stg, and
> prod, and the question usually spans all three.
>
> Kubeside is one Go binary with an embedded UI. It reads the kubeconfig already
> on your machine, connects every context, and groups objects into apps rather
> than kinds. It writes nothing to disk: the timeline is reconstructed from what
> the cluster still holds, in ReplicaSets, ControllerRevisions, Helm release
> secrets, and pod termination states, then extended live while the process
> runs.
>
> Two rules it holds itself to. Absence of knowledge is not absence of a thing:
> an empty axis is never rendered as a quiet period, a metric it could not take
> is reported as unavailable rather than as zero, and a source it could not read
> is named. And a control is never hidden for lack of permission: it is
> disabled, and it names the verb the cluster refused, resolved through
> SelfSubjectAccessReview.
>
> It refuses a long list of things on purpose: no node view, no RBAC editor, no
> CRD browser, no Helm management, no cost reporting, no topology graph. Those
> belong to a different person's tool.
>
> The reasoning is all in the repository, including the research the design came
> from and the anti-requirements drawn from other tools' issue trackers.

### r/kubernetes

Title:

> I built a Kubernetes client that only answers four questions

Body:

> Every dashboard I have used mirrors the API tree, and every one treats the
> cluster as the unit of work. As an application developer my unit of work is an
> app across qa, stg, and prod, and my four questions are: is it up, what
> changed, what do the logs say across every replica, and what config did the
> container actually get.
>
> So I built the tool for that and refused everything else. One binary, embedded
> UI, no database, no agent in the cluster, no setup: if kubectl works, it
> works. Read-only by default, with the write path gated on the cluster's own
> RBAC answer rather than on a config flag.
>
> The parts I would most like criticism on are the app-grouping heuristics
> (owner references first, then recommended labels, then Argo, then workload
> name) and the timeline reconstruction, since both fail differently on
> organically grown clusters than on clean demo ones.

### CNCF Slack

Short, in `#kubernetes-users` or `#kubernetes-novice`, and only if the channel
tolerates a project post. Read the channel's rules first.

> Sharing a small open-source tool: kubeside, a Kubernetes client scoped to the
> application developer. One binary, reads your kubeconfig, groups objects into
> apps across qa/stg/prod, and answers four questions: is my app up, what
> changed, what do the logs say across every pod, what configuration did the
> container receive. No storage layer, nothing written to disk. Apache-2.0.
> Feedback on the grouping heuristics is what I am after.

## 4. After

Watch the first hour of issues. The failure most likely to arrive is an app list
that does not match what somebody recognises as their own apps, from a cluster
whose labelling nobody controls. That is the product's central bet, and the one
question worth answering before anything else in the backlog.

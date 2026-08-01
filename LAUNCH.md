# Release and launch

The release path is live and documented below. v1.0.0 and v1.0.1 shipped
without an announcement; the posts in section 3 are written and checked and
have not been sent.

One rule before any of them goes out: what `brew install` hands over has to be
what the post describes. v1.0.1 sat on the tap for ten commits, long enough to
be missing usage columns, the app row's tag and age and restarts, the
self-hosted fonts, the light theme and the text size control. A post describing
main would have described a build nobody could install.

## 1. The site

Pages is enabled with GitHub Actions as its source, and `PAGES_ENABLED` is set,
so every push to `main` that touches the docs rebuilds and republishes
`https://kubeside.dynaum.com`.

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

## 2. Releases

Cutting one is a tag:

```
git tag -a vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z
gh run watch                       # builds the UI, then the six archives
gh release view vX.Y.Z             # a draft until you publish it
gh release edit vX.Y.Z --draft=false
```

One thing is not automatic yet, and it fails quietly. The Homebrew formula lives
in another repository, so GoReleaser needs a token with `contents: write` on
`dynaum/homebrew-tap`, in the `TAP_GITHUB_TOKEN` secret. Without it the formula
is generated and the push is skipped, which means a new release ships while
`brew install` keeps handing out the previous version and nothing looks wrong.

Until that secret exists, update `Formula/kubeside.rb` in the tap by hand from
the release's own `checksums.txt`.

Before publishing, run the binary once from the archive on a machine that is not
this one. The release workflow verifies the UI reached the binary; it does not
verify that a downloaded archive opens on a Mac with Gatekeeper.

## 3. Posts

Checked against the repository at v1.1.0. The rule stands: claim only what the
product does today, and recheck before sending, because these age with the code.

Post them on different days. The same link in two places within an hour reads as
a campaign, and the Reddit crowd notices.

### Show HN

Title, 77 characters against a limit of 80:

> Show HN: Kubeside – a Kubernetes client that shows your app, not your cluster

URL field: `https://github.com/dynaum/kubeside`

Body:

> I kept opening k9s to answer four questions and having to think in
> ReplicaSets to get to them: is my app up, what changed and when, what do the
> logs say across every pod at once, and what configuration did the container
> actually receive.
>
> The tools I tried are good at the operator's job. They mirror the API tree,
> and they treat a cluster as the unit of work: pick one, look at it, pick
> another. My unit of work is one app across qa, stg and prod, and the question
> usually spans all three.
>
> Kubeside is one Go binary with the UI embedded. It reads the kubeconfig you
> already have, connects every context, and groups objects into apps:
> recommended labels first, then the Helm release annotation, then the Argo
> instance label, then the owner chain, then the workload name. Every row says
> which rule produced it, so a list that looks wrong tells you why it looks
> wrong.
>
> It writes nothing to disk. No database, no cache, no agent in your cluster.
> The timeline is reconstructed on demand from what the cluster still holds:
> ReplicaSets, ControllerRevisions, Helm release secrets, pod termination
> states, and events still inside the apiserver TTL. That is also why it cannot
> show you last month, and it says so on the axis instead of drawing an empty
> one.
>
> Two rules I held it to.
>
> Absence of knowledge is not absence of a thing. An unknown window is hatched,
> never drawn as a quiet period. A metric it could not take reads as
> unavailable, never as zero. A kind it could not list is named.
>
> A control is never hidden for lack of permission. It is disabled, and it names
> the verb the cluster refused, resolved through SelfSubjectAccessReview.
>
> It refuses plenty on purpose: no node view, no RBAC editing, no CRD browser,
> no Helm management, no cost reporting, no topology graph. Those belong to a
> different person's tool.
>
> Not there yet: Prometheus as a metrics source is a stub, metrics-server is
> what works. There is no in-cluster team mode.
>
> What I would most like broken is the grouping. It behaves on clusters I
> control, and clusters I control have tidy labels by construction. If you point
> `kubeside --print` at something organically grown, I want to know whether the
> list reads like your apps or like noise.
>
> brew install dynaum/tap/kubeside, or binaries for macOS, Linux and Windows on
> the releases page. Apache-2.0.

One decision left in this one. The repository is agent-driven by design, and
Hacker News reacts badly when it learns that from a commenter rather than from
the author. Saying it in the post costs less than being found out in the thread.

### r/kubernetes

Title:

> I built a Kubernetes client that only answers four questions

Body:

> Every dashboard I have used mirrors the API tree: pick a kind, browse
> instances, pick a cluster from a switcher. That is the right shape for the
> person running the cluster. It is the wrong shape for the person shipping the
> app.
>
> My unit of work is one service across qa, stg and prod. My questions are:
>
> - Is it up?
> - What changed, when, and who changed it?
> - What do the logs say across every replica at once?
> - What configuration did the container actually receive?
>
> So I built the tool for those four and refused everything else.
>
> One Go binary with the UI embedded. It reads the kubeconfig you already have
> and connects every context. No database, no cache, no agent in the cluster, no
> setup step. If kubectl works, it works.
>
> It groups objects into apps rather than kinds, in this order: recommended
> labels, the Helm release annotation, the Argo instance label, the owner
> reference chain, then the workload name as a last resort. Every row tells you
> which rule produced it, so a list that looks wrong tells you why.
>
> The timeline is reconstructed on demand from history the cluster already
> keeps: ReplicaSets, ControllerRevisions, Helm release secrets, pod termination
> states, and events still inside the apiserver TTL. Nothing is stored, so it
> cannot show you last month. It marks on the axis where its knowledge ends
> instead of drawing an empty stretch and letting you read that as a quiet
> period.
>
> Read-only by default. The write path is gated on the cluster's own answer
> through SelfSubjectAccessReview rather than on a config flag, and a control
> you lack permission for is disabled with the missing verb named, never hidden.
>
> Deliberately absent: node view, RBAC editing, CRD browsing, Helm management,
> cost reporting, topology graphs.
>
> Honest gaps: Prometheus as a metrics source is a stub, metrics-server is what
> works today. There is no in-cluster team mode yet.
>
> What I want criticism on is the grouping and the timeline reconstruction. Both
> behave on clusters I built, and clusters I built have tidy labels by
> construction. They fail differently on organically grown ones. If you run
> kubeside --print against a cluster whose labelling nobody controlled, I want
> to know whether the app list reads like your apps or like noise.
>
> Apache-2.0, macOS, Linux and Windows.
>
> https://github.com/dynaum/kubeside

Read the subreddit rules first. r/kubernetes restricts self-promotion, and a
project post from an account with no history there can be removed or pushed to a
weekly thread. Check whether a flair is required.

The grouping order above is the real one, and it is worth stating precisely: the
earlier draft of this post had it backwards, claiming owner references came
first. That paragraph is the one inviting scrutiny, so it is the worst place to
be wrong. The chain is in `internal/apps/apps.go`.

### CNCF Slack

Short, in `#kubernetes-users` or `#kubernetes-novice`, and only if the channel
tolerates a project post. Read the channel's rules first.

> Sharing a small open-source tool: kubeside, a Kubernetes client scoped to the
> application developer. One binary, reads your kubeconfig, groups objects into
> apps across qa/stg/prod, and answers four questions: is my app up, what
> changed, what do the logs say across every pod, what configuration did the
> container receive. No storage layer, nothing written to disk. Apache-2.0.
> Feedback on the grouping heuristics is what I am after.

## 4. The question that still matters

Whether or not this is ever announced, one question outranks the rest of the
backlog: does the app list match what somebody recognises as their own apps, on
a cluster whose labelling nobody controlled? That is the product's central bet,
and it can only be answered by a person with such a cluster running
`kubeside --print` on it.

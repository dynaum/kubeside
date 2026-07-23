# The problem

Research conducted 2026-07-22 against GitHub issue trackers, Hacker News
threads, and the state of the ecosystem. Reaction counts and issue ages are from
that date.

## The landscape shifted in January 2026

The official Kubernetes Dashboard was archived on 2026-01-21 and moved to the
`kubernetes-retired` organization. SIG UI now points people to Headlamp as the
successor. Headlamp therefore inherits the whole community's expectations, and
its backlog reflects the load: 834 open issues, against 190 for Freelens and 135
for k9s.

## What people complain about

### Metrics are wrong or absent

The loudest category by a wide margin. Freelens's four most-upvoted open issues
are all metrics:

| Issue | Reactions | Subject |
| --- | --- | --- |
| [#466](https://github.com/freelensapp/freelens/issues/466) | 37 | Metrics Server and external Prometheus |
| [#627](https://github.com/freelensapp/freelens/issues/627) | 20 | Metrics not displayed with metrics-server |
| [#524](https://github.com/freelensapp/freelens/issues/524) | 20 | Native VictoriaMetrics support |
| [#1670](https://github.com/freelensapp/freelens/issues/1670) | 8 | Metrics Server in addition to Prometheus |

Then the bug pile: [#964](https://github.com/freelensapp/freelens/issues/964)
(pod shows double memory), [#1111](https://github.com/freelensapp/freelens/issues/1111),
[#1555](https://github.com/freelensapp/freelens/issues/1555),
[#1883](https://github.com/freelensapp/freelens/issues/1883) (CPU renders as
`0.0Ki`). Headlamp carries the same class of defect in
[#2043](https://github.com/kubernetes-sigs/headlamp/issues/2043). Lens had
[#7299](https://github.com/lensapp/lens/issues/7299) and
[#8011](https://github.com/lensapp/lens/issues/8011) years earlier.

Root cause: the entire Lens lineage hardcodes Prometheus query shapes. Point one
of these tools at metrics-server or VictoriaMetrics and values double, zero out,
or disappear.

Design consequence for kubeside: a metrics source is an interface with three
implementations from day one, and metrics-server is the default.

### Authentication eats the web UI

Headlamp's pain is concentrated here. Eight of its fifteen most-reacted open
issues are auth:

- [#1716](https://github.com/kubernetes-sigs/headlamp/issues/1716) EKS SSO, open
  since 2024-02-14, 35 comments.
- [#2848](https://github.com/kubernetes-sigs/headlamp/issues/2848) OIDC redirect
  loop on every click with Azure AD.
- [#4198](https://github.com/kubernetes-sigs/headlamp/issues/4198) OIDC
  impersonation broken on EKS.
- [#1582](https://github.com/kubernetes-sigs/headlamp/issues/1582) Flatpak
  sandbox cannot execute `aws eks get-token`.

Design consequence: in local mode the binary runs kubeconfig exec credential
plugins as a native child process with the user's real environment. The entire
sandboxing class of bug disappears. In-cluster mode inherits the OIDC problem and
ships later, once local mode is solid.

### Logs across more than one pod

Freelens [#687](https://github.com/freelensapp/freelens/issues/687) and
[#1460](https://github.com/freelensapp/freelens/issues/1460) both request
deployment, daemonset, and statefulset log views. k9s
[#1399](https://github.com/derailed/k9s/issues/1399) ("Stream closed EOF") has
been open since 2021-12-23 with 34 comments. On Hacker News a user described
opening six sessions manually to read one deployment's logs, and Aptakube's
founder led his pitch with exactly this feature. Everyone else still shells out
to `stern`.

Design consequence: whole-workload log tailing is the default. Per-pod is a
filter applied to it, never the starting point.

### Single-maintainer risk on k9s

34k stars. Contributor totals: `derailed` at 1121 commits, next human at 40. Of
the last 100 commits, 43 are dependabot and 4 are the maintainer. Meanwhile
[sorting pods by CPU or memory](https://github.com/derailed/k9s/issues/3793) has
been broken since January 2026 across 28 comments, and
[decoding a secret without base64 gymnastics](https://github.com/derailed/k9s/issues/1017)
has been the top request since 2021-01-21.

### Design requests exist, phrased as bug reports

Nobody files an issue saying "make this beautiful". They file legibility bugs and
personalization gaps, because those are the available words:

- Freelens [#1280](https://github.com/freelensapp/freelens/issues/1280) custom
  themes: 42 comments, funded with a 50 dollar bounty, and the entire thread is
  an assignment dispute between two contributors. Nothing shipped.
- Freelens [#550](https://github.com/freelensapp/freelens/issues/550) accent
  color, [#186](https://github.com/freelensapp/freelens/issues/186) filter box
  unreadable in the light theme,
  [#469](https://github.com/freelensapp/freelens/issues/469) font size.
- Headlamp #1788 "design papercuts umbrella issue",
  [#3574](https://github.com/kubernetes-sigs/headlamp/issues/3574) nodes table
  barely visible in dark theme,
  [#1835](https://github.com/kubernetes-sigs/headlamp/issues/1835) widescreen
  dashboard brainstorm where seven people typed `/assign` and nothing shipped.
- The largest thread on Headlamp's
  [2020 HN launch](https://news.ycombinator.com/item?id=25118870) is a pile-on
  against hiding buttons based on RBAC instead of disabling them with an
  explanation. Six years later, unchanged.

Neither project has design capacity. Freelens tried to buy some for 50 dollars.
Aptakube, K8Studio, and Kunobi charge money and compete purely on polish, which
demonstrates willingness to pay for what the open source options do not deliver.

Design consequence for kubeside: controls a user lacks permission for render
disabled with the missing RBAC verb named in a tooltip. Never hidden.

## The four uncovered gaps

These define the product.

### 1. Resource-centric navigation

Every tool mirrors the API tree: Workloads, then Pods, then ConfigMaps. A
developer thinks "the checkout service", spanning a Deployment, a Service, an
Ingress, two ConfigMaps, a Secret, and an HPA.
[Kubevious](https://kubevious.io/docs/features/application-centric-ui/) named
this years ago and stayed niche.

### 2. No sense of time

Everything renders now. "Why did my pod restart twenty minutes ago" has no
answer in any of these tools. k9s
[#3599](https://github.com/derailed/k9s/issues/3599) asks for a diff against the
previous version of an object. Google shipped
[KHI](https://github.com/GoogleCloudPlatform/khi) as a separate timeline viewer
because no dashboard does history. Komodor sells this commercially.

The assumption everyone makes is that history requires a database, which is why
no free tool has one and why Komodor charges for it. The assumption is wrong.
Kubernetes already retains ReplicaSets, ControllerRevisions, Helm release
secrets, pod termination states, and recent events. The history is sitting in the
cluster and no tool assembles it. That is the real gap, and closing it needs no
storage at all. See [05-architecture.md](05-architecture.md).

### 3. Effective configuration

Headlamp [#4798](https://github.com/kubernetes-sigs/headlamp/issues/4798)
requests resolved environment variables for a running pod. No tool merges `env`,
`envFrom`, ConfigMap, Secret, and downward API into one view with provenance per
row. This is the most common developer question and every tool makes you assemble
the answer by hand from four screens.

### 4. Operator sidebar, developer user

Nodes, PersistentVolumes, CRDs, Helm, and RBAC dominate the navigation. Headlamp
[#4051](https://github.com/kubernetes-sigs/headlamp/issues/4051) ("improve UX for
non-cluster-admins") acknowledges the mismatch. A developer with namespace-scoped
RBAC sees a sidebar mostly full of forbidden links.

## Validation

The grouping engine was tested against a real, organically grown cluster on
2026-07-23 rather than a fixture. 141 apps, of which 134 resolved on the first
rule of the precedence chain. Nothing was split across rows and nothing
unrelated was merged; the fallbacks were all vendor components that genuinely
ship without recommended labels.

That settles the riskiest question in the plan: developers do recognise the
output as their own services. It also surfaced two findings the plan had not
anticipated, recorded as issues #39 and #40.

## Competition

[skyhook-io/radar](https://github.com/skyhook-io/radar) launched 2026-01-20 under
Apache-2.0 and reached 2.6k stars in six months, positioned as "the missing open
source Kubernetes UI: topology, event timeline, and service traffic". Its
comparison page attacks Lens, Freelens, k9s, and Headlamp on gaps 1 and 2.

Assume Radar gets topology right. The kubeside wedge is narrower and different:
the developer's four questions, resolved configuration, whole-workload logs, and
the same application compared across environments.

## Reference points

| Tool | Stars | Open issues | License | Note |
| --- | --- | --- | --- | --- |
| k9s | 34,182 | 135 | Apache-2.0 | Terminal, single maintainer |
| kubernetes/dashboard | 15,427 | 166 | Apache-2.0 | Archived 2026-01-21 |
| Headlamp | 6,920 | 834 | Apache-2.0 | SIG UI successor, auth-heavy backlog |
| Freelens | 5,311 | 190 | MIT | Electron, Lens lineage, metrics bugs |
| Kite | 2,937 | 39 | Apache-2.0 | Web, platform positioning |
| Radar | 2,645 | 50 | Apache-2.0 | Topology and timeline, direct competitor |
| KHI | 2,071 | 21 | Apache-2.0 | Google, log timeline only |
</content>

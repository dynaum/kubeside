// Canned cluster answers, so a screenshot never depends on a real cluster.
//
// Visual regression only works when the pixels are a function of the code. A
// fixture that changes with a cluster's mood tests nothing and fails at random.

export const contexts = [
  {
    name: "qa1", current: true, state: "live", hasData: true,
    environment: "qa", risk: "low", color: "green", hazard: false, write: "allow",
  },
  {
    name: "prod-us-east", current: false, state: "live", hasData: true,
    environment: "prod", risk: "high", color: "red", hazard: true, write: "break-glass",
  },
];

// The clock the screenshots freeze at is 2026-07-25T03:00:00Z, so every
// revisionAt below is chosen to render a readable age against it.
const apps = [
  {
    namespace: "team-b", name: "search-indexer", kind: "Deployment", health: "failed",
    reason: "ImagePullBackOff", detail: 'pod search-indexer-75f4d is in ImagePullBackOff',
    groupedBy: "recommended-labels", ready: "0/2", objects: 4,
    image: "ghcr.io/acme/search-indexer:2.1.0-rc4", tag: "2.1.0-rc4",
    revisionAt: "2026-07-25T02:48:00Z", pods: 2, restarts: 0,
    // The image never pulled, so no container is running to measure.
    cpuMilli: 0, memoryBytes: 0, measured: 0,
  },
  {
    // Never scheduled, so no pod exists and the restart column shows a dash
    // rather than a zero nobody measured.
    namespace: "team-b", name: "billing", kind: "CronJob", health: "unknown",
    reason: "not scheduled", detail: "the schedule has not fired yet",
    groupedBy: "workload-name", ready: "", objects: 1,
    image: "ghcr.io/acme/billing:1.9.0", tag: "1.9.0",
    revisionAt: "2026-07-19T03:00:00Z", pods: 0, restarts: 0,
    cpuMilli: 0, memoryBytes: 0, measured: 0,
  },
  {
    namespace: "team-a", name: "checkout", kind: "Deployment", health: "healthy",
    reason: "", detail: "", groupedBy: "recommended-labels", ready: "4/4", objects: 6,
    image: "ghcr.io/acme/checkout:1.4.2", tag: "1.4.2",
    revisionAt: "2026-07-23T03:00:00Z", pods: 4, restarts: 0,
    cpuMilli: 340, memoryBytes: 1_476_395_008, measured: 4,
  },
  {
    // 5/6 hides the replica that has died fourteen times. The restart column is
    // the only place that shows on this screen.
    namespace: "team-a", name: "payments", kind: "Deployment", health: "degraded",
    reason: "one replica not ready", detail: "1 replica failing readinessProbe on :8080/healthz",
    groupedBy: "recommended-labels", ready: "5/6", objects: 8,
    image: "ghcr.io/acme/payments:3.2.1", tag: "3.2.1",
    revisionAt: "2026-07-25T01:30:00Z", pods: 6, restarts: 14,
    // One replica is down, so the total is built from five of six.
    cpuMilli: 1210, memoryBytes: 872_415_232, measured: 5,
  },
];

export const appsView = {
  context: "qa1", state: "live", scope: "cluster-wide", apps,
  metrics: { source: "metrics-server", available: true },
};

export const appDetail = {
  context: "qa1", namespace: "team-a", workload: "payments", kind: "Deployment",
  health: "degraded", reason: "one replica not ready",
  detail: "1 replica failing readinessProbe on :8080/healthz",
  ready: "5/6", image: "registry.example.com/team-a/payments:v1.8.0",
  revisionAt: "2026-07-14T09:12:00Z", restarts: 14,
  pods: [
    { name: "payments-6c8f7d-qk2z", phase: "Running", health: "failed", ready: false, restarts: 14, ageSec: 950400, reason: "CrashLoopBackOff" },
    { name: "payments-6c8f7d-m4tf", phase: "Running", health: "healthy", ready: true, restarts: 0, ageSec: 950400 },
    { name: "payments-6c8f7d-p9wd", phase: "Running", health: "healthy", ready: true, restarts: 0, ageSec: 172800 },
  ],
  timeline: {
    context: "qa1", namespace: "team-a", workload: "payments",
    entries: [
      { at: "2026-07-25T02:22:09Z", kind: "restart", title: "payments restarted", detail: "OOMKilled, exit code 137", source: "pod", pod: "payments-6c8f7d-qk2z" },
      { at: "2026-07-25T01:58:00Z", kind: "warning", title: "Unhealthy", detail: "Readiness probe failed: dial tcp i/o timeout", source: "event" },
      { at: "2026-07-24T22:10:00Z", kind: "health", title: "healthy → degraded", detail: "1 replica failing readinessProbe", source: "session" },
      { at: "2026-07-14T09:12:00Z", kind: "deploy", title: "revision 12", detail: "registry.example.com/team-a/payments:v1.8.0", source: "replicaset", revision: "12", image: "registry.example.com/team-a/payments:v1.8.0", actor: { kind: "kubectl", name: "kubectl-edit" } },
      { at: "2026-07-02T11:04:00Z", kind: "deploy", title: "revision 11", detail: "registry.example.com/team-a/payments:v1.7.4", source: "replicaset", revision: "11" },
    ],
    horizons: [
      { at: "2026-07-02T11:04:00Z", source: "replicaset", reason: "older rollouts pruned by revisionHistoryLimit; revision 11 is the oldest the cluster still holds", pruned: true },
      { at: "2026-07-25T01:40:00Z", source: "event", reason: "older events have expired from the apiserver, which keeps roughly an hour by default", pruned: true },
      { at: "2026-07-24T20:00:00Z", source: "session", reason: "kubeside started watching here; anything before this comes from the cluster's own history", pruned: false },
    ],
    gaps: [{ source: "helm", reason: "needs read access to secrets in this namespace" }],
  },
};

export const configView = {
  context: "qa1", namespace: "team-a", workload: "payments", pod: "payments-6c8f7d-m4tf",
  comparedTo: "11",
  caveat: "values are read from the ConfigMaps as they are now; a container that started before a change is still running the old value until it restarts",
  containers: [{
    name: "payments",
    values: [
      { key: "DATABASE_URL", value: "", masked: true, source: { kind: "secret", ref: "payments-db", key: "url" }, canReveal: false, revealReason: "needs get secrets/payments-db in team-a", diff: { state: "not-recoverable", reason: "Kubernetes keeps no content history for Secret payments-db" } },
      { key: "LOG_LEVEL", value: "debug", source: { kind: "configMap", ref: "payments-cfg", key: "LOG_LEVEL" }, diff: { state: "not-recoverable", reason: "Kubernetes keeps no content history for ConfigMap payments-cfg" } },
      { key: "MAX_CONNECTIONS", value: "25", source: { kind: "inline" }, diff: { state: "changed", previous: "10" } },
      { key: "POD_IP", value: "10.4.19.221", source: { kind: "downwardAPI", key: "status.podIP" }, diff: { state: "runtime", reason: "resolved when the container started" } },
      { key: "RETRY_LIMIT", value: "3", source: { kind: "inline" }, diff: { state: "unchanged" } },
    ],
    mounts: [{ path: "/etc/payments", source: { kind: "configMap", ref: "payments-rates" }, readOnly: true }],
  }],
};

export const promotionView = {
  envs: [{ name: "qa", risk: "low", context: "qa1" }, { name: "prod", risk: "high", context: "prod-us-east" }],
  rows: [
    {
      app: "search-indexer", namespace: "team-b", drift: 1, ahead: true,
      cells: [
        { env: "qa", state: "same", tag: "v3.1.0-rc4", health: "healthy", ready: "1/1", revisionAt: "2026-07-23T09:00:00Z" },
        { env: "prod", state: "ahead", tag: "v3.1.0-rc4", health: "failed", ready: "0/3", severe: true, note: "ahead of qa; nothing promoted this", revisionAt: "2026-07-25T02:07:00Z" },
      ],
    },
    {
      app: "payments", namespace: "team-a", drift: 1,
      cells: [
        { env: "qa", state: "same", tag: "v1.8.2", health: "healthy", ready: "2/2", revisionAt: "2026-07-24T09:00:00Z" },
        { env: "prod", state: "behind", tag: "v1.8.0", health: "degraded", ready: "5/6", note: "behind qa, which runs v1.8.2", revisionAt: "2026-07-14T09:12:00Z" },
      ],
    },
    {
      app: "checkout", namespace: "team-a", drift: 0,
      cells: [
        { env: "qa", state: "same", tag: "v2.14.0", health: "healthy", ready: "2/2", revisionAt: "2026-07-25T00:00:00Z" },
        { env: "prod", state: "same", tag: "v2.14.0", health: "healthy", ready: "6/6", digestPending: true, revisionAt: "2026-07-25T00:00:00Z" },
      ],
    },
    {
      app: "notifications", namespace: "team-b", drift: 1,
      cells: [
        { env: "qa", state: "same", tag: "v0.9.1", health: "healthy", ready: "2/2", revisionAt: "2026-07-24T12:00:00Z" },
        { env: "prod", state: "absent", note: "not deployed here" },
      ],
    },
  ],
  summary: { apps: 4, drifted: 3, ahead: 1 },
};

export const capabilities = {
  context: "qa1", namespace: "team-a",
  can: {
    "create pods/exec in team-a": { allowed: true },
    "create pods/portforward in team-a": { allowed: true },
    "delete pods in team-a": { allowed: false, reason: "needs delete pods in team-a" },
    "get pods/log in team-a": { allowed: true },
    "patch deployments in team-a": { allowed: true },
  },
};

export const forwards = [];

// A break-glass environment with the window shut: what the unlock dialog shows
// before anybody states a reason.
export const gateView = {
  gate: {
    environment: "prod",
    risk: "high",
    policy: "break-glass",
    permitted: false,
    require: "break-glass",
    reason: "prod is behind break-glass; unlock it with a stated reason to act here",
    blast: { pods: 0, unknown: true },
  },
  permission: { allowed: true },
};

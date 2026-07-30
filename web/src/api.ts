// The wire types mirror internal/api/view.go. The session token lives in the
// URL kubeside opened; every request carries it.

export interface ContextView {
  name: string;
  current: boolean;
  state: string;
  hasData: boolean;
  ageSec?: number;
  error?: string;
  // Resolved by the backend from the config file, or from the context name
  // when no file binds it.
  environment: string;
  risk: string;
  color: string;
  hazard: boolean;
  write: string;
}

export interface AppView {
  namespace: string;
  name: string;
  kind: string;
  health: string;
  reason: string;
  detail: string;
  groupedBy: string;
  managedBy?: string;
  ready: string;
  objects: number;
}

export interface TimelineActor {
  kind?: string;
  name?: string;
}

export interface TimelineEntry {
  at: string;
  kind: string; // deploy, release, restart, warning, health, scale, run
  title: string;
  detail?: string;
  source: string;
  revision?: string;
  image?: string;
  pod?: string;
  actor?: TimelineActor;
}

// A horizon is where one source ran out. Pruned distinguishes a cut in
// something that was known from the point where watching simply began.
export interface TimelineHorizon {
  at: string;
  source: string;
  reason: string;
  pruned: boolean;
}

// A gap is a source that could not be read at all, with what it needs.
export interface TimelineGap {
  source: string;
  reason: string;
}

export interface TimelineView {
  context: string;
  namespace: string;
  workload: string;
  entries: TimelineEntry[];
  horizons?: TimelineHorizon[];
  gaps?: TimelineGap[];
}

export interface PodView {
  name: string;
  phase?: string;
  health: string;
  ready: boolean;
  restarts: number;
  ageSec?: number;
  reason?: string;
}

export interface AppDetailView {
  context: string;
  namespace: string;
  workload: string;
  kind: string;
  health: string;
  reason?: string;
  detail?: string;
  ready?: string;
  image?: string;
  revisionAt?: string;
  restarts: number;
  pods: PodView[];
  timeline: TimelineView;
}

export interface ResolvedSource {
  kind: string; // inline, configMap, secret, downwardAPI, resourceField
  ref?: string;
  key?: string;
}

export interface ResolvedValue {
  key: string;
  value: string;
  source: ResolvedSource;
  masked?: boolean;
  missing?: boolean;
  overrides?: boolean;
  reason?: string;
  canReveal?: boolean;
  revealReason?: string;
  diff?: ResolvedDiff;
}

// How a value compares with the previous revision, scoped to what the cluster
// actually preserved. "not-recoverable" is never "unchanged": Kubernetes keeps
// no content history for ConfigMaps or Secrets.
export interface ResolvedDiff {
  state?: string; // unchanged, changed, added, removed, source-changed, not-recoverable, runtime
  previous?: string;
  reason?: string;
}

export interface RevealView {
  secret: string;
  key: string;
  value?: string;
  binary?: boolean;
  note?: string;
}

export interface ResolvedMount {
  path: string;
  source: ResolvedSource;
  masked?: boolean;
  readOnly?: boolean;
}

export interface ResolvedContainer {
  name: string;
  init?: boolean;
  image?: string;
  values: ResolvedValue[];
  mounts?: ResolvedMount[];
}

export interface ConfigView {
  context: string;
  namespace: string;
  workload: string;
  pod: string;
  containers: ResolvedContainer[];
  caveat?: string;
  comparedTo?: string;
}

export interface CrossRow {
  key: string;
  left?: string;
  right?: string;
  leftUnset?: boolean;
  rightUnset?: boolean;
  masked?: boolean;
  leftDigest?: string;
  rightDigest?: string;
  class: string; // match, expected, drift, suspicious, missing, or empty
  reason?: string;
}

export interface DiffSide {
  context: string;
  namespace: string;
  workload: string;
  pod?: string;
  env: { name: string; risk: string };
}

export interface DiffView {
  left: DiffSide;
  right: DiffSide;
  container: string;
  rows: CrossRow[];
  summary: {
    drift: number;
    suspicious: number;
    missing: number;
    expected: number;
    match: number;
    unknown: number;
  };
}

export interface ForwardView {
  id: string;
  context: string;
  namespace: string;
  workload: string;
  pod: string;
  remotePort: number;
  localPort: number;
  address: string;
  state: string;
  environment?: string;
  risk?: string;
  startedAt: string;
  error?: string;
}

export interface Permission {
  allowed: boolean;
  reason?: string;
}

// What this reader may do in one namespace of one context. Controls render
// disabled with the verb named, never hidden.
export interface CapabilitiesView {
  context: string;
  namespace: string;
  can: Record<string, Permission>;
}

export interface GateBlast {
  pods: number;
  summary?: string;
  unknown?: boolean;
}

export interface Gate {
  permitted: boolean;
  require?: string; // "", typed-name, break-glass
  confirm?: string;
  environment: string;
  risk: string;
  policy: string;
  reason?: string;
  kubectl?: string;
  blast: GateBlast;
  unlockedUntil?: string;
}

export interface GateView {
  gate: Gate;
  permission: Permission;
}

export interface PromotionCell {
  env: string;
  namespace?: string;
  state: string; // same, behind, ahead, differs, digest-differs, absent, denied
  tag?: string;
  image?: string;
  digest?: string;
  digestPending?: boolean;
  health?: string;
  ready?: string;
  revisionAt?: string;
  note?: string;
  severe?: boolean;
}

export interface PromotionRow {
  app: string;
  namespace: string;
  cells: PromotionCell[];
  drift: number;
  ahead?: boolean;
}

export interface PromotionView {
  envs: { name: string; risk: string; context?: string }[];
  rows: PromotionRow[];
  summary: { apps: number; drifted: number; ahead: number };
  unreachable?: string[];
}

export interface MetricsInfo {
  source: string;
  available: boolean;
  reason?: string;
}

export interface AppsView {
  context: string;
  state: string;
  scope: string;
  reason?: string;
  partial?: string[];
  apps: AppView[];
  error?: string;
  metrics: MetricsInfo;
}

// No token travels in a URL. The server hands one over once, as an HttpOnly
// cookie the browser attaches to same-origin requests on its own, so nothing
// here has to hold a credential and nothing lands in history.

// post carries a body rather than query parameters, because a secret value
// must never travel in a URL where it would land in history and logs.
async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text();
    let message = text;
    try {
      message = (JSON.parse(text) as { error?: string }).error ?? text;
    } catch {
      // The body was not JSON; the raw text is the best message available.
    }
    throw new Error(message || res.statusText);
  }
  return res.json() as Promise<T>;
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status}: ${body || res.statusText}`);
  }
  return res.json() as Promise<T>;
}

export const api = {
  contexts: () => get<ContextView[]>("/api/contexts"),
  apps: (context: string) => get<AppsView>(`/api/apps?context=${encodeURIComponent(context)}`),
  app: (context: string, namespace: string, workload: string) =>
    get<AppDetailView>(
      `/api/app?context=${encodeURIComponent(context)}&namespace=${encodeURIComponent(namespace)}&workload=${encodeURIComponent(workload)}`,
    ),
  reveal: (context: string, namespace: string, secret: string, key: string, workload: string) =>
    post<RevealView>("/api/secret", { context, namespace, secret, key, workload }),
  promotion: () => get<PromotionView>("/api/promotion"),
  gate: (req: {
    context: string;
    namespace: string;
    verb: string;
    resource: string;
    name: string;
    // The target, when it is known before the action runs. It is what turns the
    // dialog's equivalent command into one that would actually work.
    pod?: string;
    container?: string;
    remotePort?: number;
    unlock?: string;
  }) => post<GateView>("/api/gate", req),
  can: (context: string, namespace: string) =>
    get<CapabilitiesView>(
      `/api/can?context=${encodeURIComponent(context)}&namespace=${encodeURIComponent(namespace)}`,
    ),
  forwards: () => get<ForwardView[]>("/api/forwards"),
  startForward: (
    context: string, namespace: string, workload: string, remotePort: number, confirm?: string,
  ) => post<ForwardView>("/api/forwards", { context, namespace, workload, remotePort, confirm }),
  stopForward: (id: string) => post<ForwardView[]>("/api/forwards", { stop: id }),
  diff: (context: string, namespace: string, workload: string, other: string) =>
    get<DiffView>(
      `/api/diff?context=${encodeURIComponent(context)}&namespace=${encodeURIComponent(namespace)}` +
        `&workload=${encodeURIComponent(workload)}&other=${encodeURIComponent(other)}`,
    ),
  config: (context: string, namespace: string, workload: string) =>
    get<ConfigView>(
      `/api/config?context=${encodeURIComponent(context)}&namespace=${encodeURIComponent(namespace)}&workload=${encodeURIComponent(workload)}`,
    ),
};

// The live path. One websocket per tab, carrying deltas; the browser never
// polls. The wire types mirror internal/api/delta.go.

import { useEffect, useState } from "react";
import { api, type AppView, type AppsView, type MetricsInfo } from "./api";
import { applyBatch } from "./logs";

export interface AppsMeta {
  state: string;
  scope: string;
  reason?: string;
  partial?: string[];
  error?: string;
  metrics: MetricsInfo;
}

export interface AppsPatch {
  meta?: AppsMeta;
  added?: AppView[];
  changed?: AppView[];
  removed?: string[];
}

export interface LogLine {
  seq: number;
  time?: string;
  pod: string;
  container: string;
  text: string;
  previous?: boolean;
  truncated?: boolean;
  late?: boolean;
}

export interface LogEdge {
  kind: string; // horizon, restart, gone, error, ended
  pod?: string;
  container?: string;
  time?: string;
  reason: string;
}

export interface LogsBatch {
  lines?: LogLine[];
  edges?: LogEdge[];
  dropped?: number;
  reset?: boolean;
}

/** LogsRequest is one log subscription. Every field is part of its identity. */
export interface LogsRequest {
  context: string;
  namespace: string;
  workload: string;
  pods?: string[];
  containers?: string[];
  includeSidecars?: boolean;
  includeInit?: boolean;
  previous?: boolean;
  tail?: number;
}

export interface ServerMessage {
  type: "snapshot" | "patch" | "logs" | "error";
  view: string;
  context: string;
  key?: string;
  seq: number;
  snapshot?: AppsView;
  patch?: AppsPatch;
  logs?: LogsBatch;
  message?: string;
}

const appKey = (a: AppView) => `${a.namespace}/${a.name}`;

// applyPatch folds a patch into a view. Absent means unchanged, never empty: a
// patch that omits `removed` did not delete every app.
export function applyPatch(view: AppsView, patch: AppsPatch): AppsView {
  let apps = view.apps;

  if (patch.removed?.length) {
    const gone = new Set(patch.removed);
    apps = apps.filter((a) => !gone.has(appKey(a)));
  }
  if (patch.changed?.length) {
    const byKey = new Map(patch.changed.map((a) => [appKey(a), a]));
    apps = apps.map((a) => byKey.get(appKey(a)) ?? a);
  }
  if (patch.added?.length) {
    apps = [...apps, ...patch.added];
  }

  const next: AppsView = { ...view, apps };
  if (patch.meta) {
    // Metadata travels whole, so a field the server dropped is a field that no
    // longer applies: assign every one, including the absent ones.
    next.state = patch.meta.state;
    next.scope = patch.meta.scope;
    next.reason = patch.meta.reason;
    next.partial = patch.meta.partial;
    next.error = patch.meta.error;
    next.metrics = patch.meta.metrics;
  }
  return next;
}

/** How the live connection is doing, so the UI can say so rather than imply it. */
export type Liveness = "connecting" | "live" | "retrying";

type Handler = (m: ServerMessage) => void;

/** Subscription is what the socket sends to open one view. */
type Subscription = { view: string; context: string } & Partial<LogsRequest>;

// subscriptionKey mirrors the server: two windows asking for the same thing
// share a feed, and any difference in the filters is a different subscription.
function subscriptionKey(s: Subscription): string {
  if (s.view !== "logs") return `${s.view}|${s.context}`;
  return [
    s.view, s.context, `${s.namespace}/${s.workload}`,
    `pods=${(s.pods ?? []).join(",")}`,
    `containers=${(s.containers ?? []).join(",")}`,
    `sidecars=${!!s.includeSidecars}`,
    `init=${!!s.includeInit}`,
    `previous=${!!s.previous}`,
    `tail=${s.tail ?? 0}`,
  ].join("|");
}

const RETRY_MIN = 500;
const RETRY_MAX = 10_000;

// One socket per tab, shared by every view that subscribes. Opening a second
// connection per screen would multiply the handshake and the server's bookkeeping
// for no gain: the protocol already multiplexes on view and context.
class Stream {
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Set<Handler>>();
  private open_ = new Map<string, Subscription>();
  private watchers = new Set<(l: Liveness) => void>();
  private retry = RETRY_MIN;
  private timer: number | undefined;
  private state: Liveness = "connecting";

  subscribe(sub: Subscription, h: Handler): () => void {
    const key = subscriptionKey(sub);
    const set = this.handlers.get(key) ?? new Set<Handler>();
    set.add(h);
    this.handlers.set(key, set);
    this.open_.set(key, sub);

    this.open();
    this.send({ type: "subscribe", ...sub });

    return () => {
      const cur = this.handlers.get(key);
      cur?.delete(h);
      if (cur && cur.size === 0) {
        this.handlers.delete(key);
        this.open_.delete(key);
        // Telling the server nobody is watching is what stops it reading that
        // cluster, and closes the log streams it had open. Silence would leave
        // both running for a screen nobody has.
        this.send({ type: "unsubscribe", ...sub });
      }
      if (this.handlers.size === 0) this.close();
    };
  }

  watch(w: (l: Liveness) => void): () => void {
    this.watchers.add(w);
    w(this.state);
    return () => { this.watchers.delete(w); };
  }

  private setState(l: Liveness) {
    this.state = l;
    for (const w of this.watchers) w(l);
  }

  private open() {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) return;

    // The session cookie rides the upgrade like any same-origin request, so
    // the socket needs no credential of its own.
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${window.location.host}/api/stream`);
    this.ws = ws;
    this.setState(this.retry === RETRY_MIN ? "connecting" : "retrying");

    ws.onopen = () => {
      this.retry = RETRY_MIN;
      this.setState("live");
      // A reconnect resubscribes everything the tab was watching, and each
      // subscription answers with a fresh snapshot, so a gap in the stream
      // cannot leave a stale screen behind.
      for (const sub of this.open_.values()) {
        this.send({ type: "subscribe", ...sub });
      }
    };

    ws.onmessage = (ev) => {
      let m: ServerMessage;
      try {
        m = JSON.parse(ev.data as string) as ServerMessage;
      } catch {
        return;
      }
      // The server echoes the subscription key, so a tab holding two log
      // subscriptions on one context routes by identity instead of guessing.
      const target = m.key ?? `${m.view}|${m.context}`;
      for (const h of this.handlers.get(target) ?? []) h(m);
    };

    ws.onclose = () => {
      this.ws = null;
      if (this.handlers.size === 0) return;
      this.setState("retrying");
      this.timer = window.setTimeout(() => this.open(), this.retry);
      this.retry = Math.min(this.retry * 2, RETRY_MAX);
    };
  }

  private close() {
    window.clearTimeout(this.timer);
    this.retry = RETRY_MIN;
    this.ws?.close();
    this.ws = null;
  }

  private send(m: { type: string } & Subscription) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(m));
  }
}

export const stream = new Stream();

export interface AppsStream {
  view: AppsView | null;
  error: string | null;
  liveness: Liveness;
}

// useApps subscribes to one context and keeps the view current.
//
// If the socket closes before it ever delivered a snapshot, one REST read fills
// the screen so a blocked websocket does not leave the developer staring at a
// spinner. The stream keeps retrying underneath and takes over when it lands.
export function useApps(context: string): AppsStream {
  const [view, setView] = useState<AppsView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [liveness, setLiveness] = useState<Liveness>("connecting");

  useEffect(() => {
    setView(null);
    setError(null);

    let alive = true;
    let gotSnapshot = false;
    let fellBack = false;

    const stopWatching = stream.watch((l) => {
      if (!alive) return;
      setLiveness(l);
      if (l === "retrying" && !gotSnapshot && !fellBack) {
        fellBack = true;
        api.apps(context)
          .then((v) => { if (alive && !gotSnapshot) setView(v); })
          .catch((e) => { if (alive && !gotSnapshot) setError(String(e)); });
      }
    });

    const unsubscribe = stream.subscribe({ view: "apps", context }, (m) => {
      if (!alive) return;
      switch (m.type) {
        case "snapshot":
          gotSnapshot = true;
          setError(null);
          setView(m.snapshot ?? null);
          break;
        case "patch":
          setView((prev) => (prev && m.patch ? applyPatch(prev, m.patch) : prev));
          break;
        case "error":
          setError(m.message ?? "the live stream reported an error");
          break;
      }
    });

    return () => {
      alive = false;
      stopWatching();
      unsubscribe();
    };
  }, [context]);

  return { view, error, liveness };
}

export interface LogsStream {
  lines: LogLine[];
  edges: LogEdge[];
  dropped: number;
  error: string | null;
  liveness: Liveness;
}

// useLogs subscribes to one workload's merged output.
//
// The request object is destructured into the dependency list rather than
// compared by reference, so a parent re-render does not tear down and reopen
// every log stream on the cluster.
export function useLogs(req: LogsRequest): LogsStream {
  const [lines, setLines] = useState<LogLine[]>([]);
  const [edges, setEdges] = useState<LogEdge[]>([]);
  const [dropped, setDropped] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [liveness, setLiveness] = useState<Liveness>("connecting");

  const pods = (req.pods ?? []).join(",");
  const containers = (req.containers ?? []).join(",");

  useEffect(() => {
    setLines([]);
    setEdges([]);
    setDropped(0);
    setError(null);

    let alive = true;
    const stopWatching = stream.watch((l) => { if (alive) setLiveness(l); });

    const unsubscribe = stream.subscribe({ view: "logs", ...req }, (m) => {
      if (!alive) return;
      if (m.type === "error") {
        setError(m.message ?? "the log stream reported an error");
        return;
      }
      if (m.type !== "logs" || !m.logs) return;

      const batch = m.logs;
      setError(null);
      setLines((prev) => applyBatch(prev, batch));
      if (batch.reset) setEdges(batch.edges ?? []);
      else if (batch.edges?.length) setEdges((prev) => [...prev, ...batch.edges!]);
      if (batch.dropped) setDropped(batch.dropped);
    });

    return () => {
      alive = false;
      stopWatching();
      unsubscribe();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    req.context, req.namespace, req.workload, pods, containers,
    req.includeSidecars, req.includeInit, req.previous, req.tail,
  ]);

  return { lines, edges, dropped, error, liveness };
}

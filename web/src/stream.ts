// The live path. One websocket per tab, carrying deltas; the browser never
// polls. The wire types mirror internal/api/delta.go.

import { useEffect, useState } from "react";
import { api, type AppView, type AppsView, type MetricsInfo } from "./api";

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

export interface ServerMessage {
  type: "snapshot" | "patch" | "error";
  view: string;
  context: string;
  seq: number;
  snapshot?: AppsView;
  patch?: AppsPatch;
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

const RETRY_MIN = 500;
const RETRY_MAX = 10_000;

// One socket per tab, shared by every view that subscribes. Opening a second
// connection per screen would multiply the handshake and the server's bookkeeping
// for no gain: the protocol already multiplexes on view and context.
class Stream {
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Set<Handler>>();
  private watchers = new Set<(l: Liveness) => void>();
  private retry = RETRY_MIN;
  private timer: number | undefined;
  private state: Liveness = "connecting";

  subscribe(view: string, context: string, h: Handler): () => void {
    const key = `${view}|${context}`;
    const set = this.handlers.get(key) ?? new Set<Handler>();
    set.add(h);
    this.handlers.set(key, set);

    this.open();
    this.send({ type: "subscribe", view, context });

    return () => {
      const cur = this.handlers.get(key);
      cur?.delete(h);
      if (cur && cur.size === 0) {
        this.handlers.delete(key);
        // Telling the server nobody is watching is what stops it reading that
        // cluster. Silence would leave a poller running for a closed screen.
        this.send({ type: "unsubscribe", view, context });
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

    const t = new URLSearchParams(window.location.search).get("t") ?? "";
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${proto}//${window.location.host}/api/stream?t=${encodeURIComponent(t)}`);
    this.ws = ws;
    this.setState(this.retry === RETRY_MIN ? "connecting" : "retrying");

    ws.onopen = () => {
      this.retry = RETRY_MIN;
      this.setState("live");
      // A reconnect resubscribes everything the tab was watching, and each
      // subscription answers with a fresh snapshot, so a gap in the stream
      // cannot leave a stale screen behind.
      for (const key of this.handlers.keys()) {
        const [view, context] = key.split("|");
        this.send({ type: "subscribe", view, context });
      }
    };

    ws.onmessage = (ev) => {
      let m: ServerMessage;
      try {
        m = JSON.parse(ev.data as string) as ServerMessage;
      } catch {
        return;
      }
      for (const h of this.handlers.get(`${m.view}|${m.context}`) ?? []) h(m);
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

  private send(m: { type: string; view: string; context: string }) {
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

    const unsubscribe = stream.subscribe("apps", context, (m) => {
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

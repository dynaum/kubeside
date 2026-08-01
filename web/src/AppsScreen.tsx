import { useEffect, useMemo, useState } from "react";
import { type AppsView, type AppView, type ContextView } from "./api";
import { Glyph } from "./Status";
import { envToken } from "./health";
import { restartCell, revisionAge, tagCell } from "./rows";
import { useApps, type Liveness } from "./stream";

const ATTENTION: Record<string, number> = {
  failed: 0, degraded: 1, progressing: 2, unknown: 3, healthy: 4,
};

export function AppsScreen({
  context,
  onOpenLogs,
  onOpenApp,
}: {
  context: ContextView;
  onOpenLogs: (namespace: string, workload: string) => void;
  onOpenApp: (namespace: string, workload: string) => void;
}) {
  const [filter, setFilter] = useState("");
  // The screen is a subscription, not a fetch: one snapshot, then patches for
  // as long as it is on screen.
  const { view, error: err, liveness } = useApps(context.name);

  const env = envToken(context);

  const rows = useMemo(() => {
    if (!view) return [];
    const f = filter.trim().toLowerCase();
    const filtered = f
      ? view.apps.filter((a) => a.name.toLowerCase().includes(f) || a.namespace.toLowerCase().includes(f))
      : view.apps;
    // Worst-first, then namespace/name: the few apps that need a human float up.
    return [...filtered].sort((a, b) => {
      const at = (ATTENTION[a.health] ?? 5) - (ATTENTION[b.health] ?? 5);
      if (at !== 0) return at;
      if (a.namespace !== b.namespace) return a.namespace < b.namespace ? -1 : 1;
      return a.name < b.name ? -1 : 1;
    });
  }, [view, filter]);

  return (
    <>
      <div className={`topbar env-edge${context.hazard ? " hazard" : ""}`} data-env={env}>
        <span className="env-chip" title={`${context.risk} risk · writes ${context.write}`}>{context.environment}</span>
        <div className="crumb">
          <span className="up">{context.name}</span>
          <span className="sep">/</span>
          <span className="cur">apps</span>
        </div>
        {view && <ScopeNote view={view} />}
        <span className="spacer" />
        <LiveDot liveness={liveness} />
        <span style={{ color: "var(--fg-4)", fontSize: 11 }}>
          <span className="kbd">⌘</span> <span className="kbd">K</span>
        </span>
        <input
          className="field"
          placeholder="filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      </div>

      <div className="page">
        {err && <Empty head="Could not load apps" body={err} />}
        {!err && !view && <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>connecting to {context.name}…</span></div>}
        {view && <Body view={view} rows={rows} totalShown={rows.length} onOpenLogs={onOpenLogs} onOpenApp={onOpenApp} />}
      </div>
    </>
  );
}

// The age column is derived from a moment on the wire, so it only advances when
// something re-renders. A quiet cluster sends no patches for minutes, and a
// column reading "2m" half an hour later is a small lie. One tick keeps it
// honest without asking the server for anything.
function useMinuteTick(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(id);
  }, []);
  return now;
}

function Body({
  view, rows, onOpenLogs, onOpenApp,
}: {
  view: AppsView;
  rows: AppView[];
  totalShown: number;
  onOpenLogs: (namespace: string, workload: string) => void;
  onOpenApp: (namespace: string, workload: string) => void;
}) {
  const now = useMinuteTick();

  if (!hasData(view.state)) {
    return (
      <Empty
        head={`${view.context} — ${view.state}`}
        body={view.error ?? "Nothing is known about this cluster yet. This is different from an empty cluster."}
      />
    );
  }
  if (view.apps.length === 0) {
    return <Empty head="No apps in scope" body={`Scope: ${view.scope}`} />;
  }

  const tally = healthTally(view.apps);

  return (
    <>
      <div className="page-head">
        <h1 className="page-title">Apps</h1>
        <span className="page-sub">{view.apps.length} workloads</span>
        <span className="spacer" />
        <span className="page-sub">{tally}</span>
      </div>

      {view.partial && view.partial.length > 0 && (
        <div className="banner" style={{ marginBottom: "var(--s4)" }}>
          <span className="st st-unknown"><i className="glyph" /></span>
          <span>Some kinds were not readable: <strong>{view.partial.join(", ")}</strong>. Rows for them may be missing.</span>
        </div>
      )}

      <table className="tbl">
        <thead>
          <tr>
            <th style={{ width: 24 }}></th>
            <th>Namespace</th>
            <th>App</th>
            <th>Kind</th>
            <th className="r">Ready</th>
            <th>Tag</th>
            <th className="r">Age</th>
            <th className="r" title="container restarts across this app's pods, for the lifetime of those pods">Restarts</th>
            <th>Why</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={`${a.namespace}/${a.name}`}>
              <td><Glyph health={a.health} /></td>
              <td className="ns">{a.namespace}</td>
              <td className="name">
                <button
                  className="tab"
                  style={{ padding: 0, textTransform: "none", letterSpacing: 0, fontFamily: "var(--font-mono)", fontSize: 12, color: "var(--fg)" }}
                  onClick={() => onOpenApp(a.namespace, a.name)}
                  // Why this row is one app stays reachable without spending a
                  // column on it. A list that looks wrong should still be able
                  // to say why it looks wrong.
                  title={`grouped by ${a.groupedBy} · ${a.objects} objects`}
                >
                  {a.name}
                </button>
              </td>
              <td className="dim">
                {a.kind}
                {a.managedBy && <span className="tag tag-managed" style={{ marginLeft: 6 }}>via {a.managedBy}</span>}
              </td>
              <td className="r ratio">{a.ready || <span className="dim">—</span>}</td>
              <td className={`mono${a.tag ? "" : " dim"}`} style={{ fontSize: 11 }} title={a.image || undefined}>
                {tagCell(a.tag)}
              </td>
              <td className="r mono dim" style={{ fontSize: 11 }} title={a.revisionAt || undefined}>
                {revisionAge(a.revisionAt, now)}
              </td>
              <td className="r mono"><Restarts pods={a.pods} restarts={a.restarts} /></td>
              <td className="dim" style={{ fontSize: 11, whiteSpace: "normal" }}>
                {a.health === "healthy" ? "" : a.detail}
              </td>
              <td className="r">
                <button className="btn" onClick={() => onOpenLogs(a.namespace, a.name)}>logs</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

// A restart count above zero is the signal a ready ratio hides: 4/4 says
// nothing about the replica that has died six times today.
function Restarts({ pods, restarts }: { pods: number; restarts: number }) {
  const cell = restartCell(pods, restarts);
  return (
    <span
      className={cell.warn ? "" : "dim"}
      style={cell.warn ? { color: "var(--warn)" } : undefined}
      title={cell.warn ? `${restarts} container restarts across ${pods} pods` : undefined}
    >
      {cell.text}
    </span>
  );
}

// Whether the screen is actually live is a fact the developer needs, not a
// detail to hide. A retrying socket says so; it never leaves an old list
// looking current.
function LiveDot({ liveness }: { liveness: Liveness }) {
  const cls = liveness === "live" ? "st-ok" : liveness === "connecting" ? "st-prog" : "st-unknown";
  const label = liveness === "live" ? "live" : liveness === "connecting" ? "connecting" : "reconnecting";
  return (
    <span className={`st ${cls}`} title={liveness === "live" ? "receiving live updates" : "not receiving updates"}>
      <i className="glyph" />
      <span className="label">{label}</span>
    </span>
  );
}

function ScopeNote({ view }: { view: AppsView }) {
  const parts = [`scope: ${view.scope}`];
  if (view.metrics) parts.push(view.metrics.available ? `metrics: ${view.metrics.source}` : "metrics: none");
  return <span className="page-sub mono" style={{ fontSize: 11 }}>{parts.join("  ·  ")}</span>;
}

function Empty({ head, body }: { head: string; body: string }) {
  return (
    <div className="empty">
      <div className="head">{head}</div>
      <div className="mono" style={{ fontSize: 12 }}>{body}</div>
    </div>
  );
}

function hasData(state: string): boolean {
  return state === "live" || state === "stale";
}

function healthTally(apps: AppView[]): string {
  const t: Record<string, number> = {};
  for (const a of apps) t[a.health] = (t[a.health] ?? 0) + 1;
  const order = ["failed", "degraded", "progressing", "unknown", "healthy"];
  return order.filter((k) => t[k]).map((k) => `${t[k]} ${k}`).join(" · ");
}

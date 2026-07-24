import { useMemo, useState } from "react";
import { type AppsView, type AppView } from "./api";
import { Glyph } from "./Status";
import { envKey } from "./health";
import { useApps, type Liveness } from "./stream";

const ATTENTION: Record<string, number> = {
  failed: 0, degraded: 1, progressing: 2, unknown: 3, healthy: 4,
};

export function AppsScreen({ context }: { context: string }) {
  const [filter, setFilter] = useState("");
  // The screen is a subscription, not a fetch: one snapshot, then patches for
  // as long as it is on screen.
  const { view, error: err, liveness } = useApps(context);

  const env = envKey(context);

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
      <div className={`topbar env-edge${env === "prod" || env === "unc" ? " hazard" : ""}`} data-env={env}>
        <span className="env-chip">{context}</span>
        <div className="crumb">
          <span className="cur">apps</span>
        </div>
        {view && <ScopeNote view={view} />}
        <span className="spacer" />
        <LiveDot liveness={liveness} />
        <input
          className="field"
          placeholder="filter"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
      </div>

      <div className="page">
        {err && <Empty head="Could not load apps" body={err} />}
        {!err && !view && <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>connecting to {context}…</span></div>}
        {view && <Body view={view} rows={rows} totalShown={rows.length} />}
      </div>
    </>
  );
}

function Body({ view, rows }: { view: AppsView; rows: AppView[]; totalShown: number }) {
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
            <th className="r">Objects</th>
            <th>Grouped by</th>
            <th>Why</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={`${a.namespace}/${a.name}`}>
              <td><Glyph health={a.health} /></td>
              <td className="ns">{a.namespace}</td>
              <td className="name">{a.name}</td>
              <td className="dim">
                {a.kind}
                {a.managedBy && <span className="tag tag-managed" style={{ marginLeft: 6 }}>via {a.managedBy}</span>}
              </td>
              <td className="r ratio">{a.ready || <span className="dim">—</span>}</td>
              <td className="r mono">{a.objects}</td>
              <td className="dim mono" style={{ fontSize: 11 }}>{a.groupedBy}</td>
              <td className="dim" style={{ fontSize: 11, whiteSpace: "normal" }}>
                {a.health === "healthy" ? "" : a.detail}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
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

import { useEffect, useState } from "react";
import { api, type AppDetailView, type ContextView, type PodView, type TimelineEntry } from "./api";
import { envToken, healthClass } from "./health";
import { Glyph } from "./Status";
import { age, clock, laneEntries, LANES, position, span, ticks, type Span } from "./detail";

// Screen 2. What changed, and when.
//
// Everything on this screen was assembled from the cluster's own records or
// watched happen while kubeside ran. Nothing was stored, which is why two
// developers opening it see the same history.

export function AppDetailScreen({
  context,
  namespace,
  workload,
  onOpenLogs,
  onBack,
}: {
  context: ContextView;
  namespace: string;
  workload: string;
  onOpenLogs: () => void;
  onBack: () => void;
}) {
  const [view, setView] = useState<AppDetailView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setView(null);
    setErr(null);
    let alive = true;
    api.app(context.name, namespace, workload)
      .then((v) => { if (alive) setView(v); })
      .catch((e) => { if (alive) setErr(String(e)); });
    return () => { alive = false; };
  }, [context.name, namespace, workload]);

  const env = envToken(context);

  return (
    <>
      <div className={`topbar env-edge${context.hazard ? " hazard" : ""}`} data-env={env}>
        <span className="env-chip">{context.environment}</span>
        <div className="crumb">
          <button className="tab" style={{ padding: 0 }} onClick={onBack}>apps</button>
          <span className="sep">/</span>
          <span className="up">{namespace}</span>
          <span className="sep">/</span>
          <span className="cur">{workload}</span>
        </div>
        {view && (
          <span className={`st ${healthClass(view.health)}`}>
            <i className="glyph" />
            <span className="label">{view.health}</span>
          </span>
        )}
        <span className="spacer" />
        <button className="btn" onClick={onOpenLogs}>Logs</button>
      </div>

      <div className="page">
        {err && (
          <div className="empty">
            <div className="head">Could not open {workload}</div>
            <div className="mono" style={{ fontSize: 12 }}>{err}</div>
          </div>
        )}
        {!err && !view && (
          <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>reconstructing history…</span></div>
        )}
        {view && <Body view={view} />}
      </div>
    </>
  );
}

function Body({ view }: { view: AppDetailView }) {
  const entries = view.timeline.entries ?? [];
  const horizons = view.timeline.horizons ?? [];
  const gaps = view.timeline.gaps ?? [];
  const s = span(entries, horizons, Date.now());

  return (
    <>
      <div className="tabs">
        <span className="tab sel">Timeline</span>
      </div>

      <div style={{ display: "flex", gap: "var(--s4)", marginBottom: "var(--s5)" }}>
        <Stat label="Ready" value={view.ready || "—"} />
        <Stat label="Running version" value={shortImage(view.image)} title={view.image} />
        <Stat label="Revision age" value={view.revisionAt ? age(secondsSince(view.revisionAt)) : "—"} />
        <Stat label="Restarts" value={String(view.restarts)} warn={view.restarts > 0} />
        <div className="frame" style={{ flex: 1.4 }}>
          <div className="frame-body" style={{ padding: "var(--s3) var(--s4)" }}>
            <div className="stat">Why {view.health}</div>
            <div style={{ fontSize: 12, color: "var(--fg-2)", marginTop: 3 }}>
              {view.detail || view.reason || "nothing needs attention"}
            </div>
          </div>
        </div>
      </div>

      <div className="frame">
        <div className="frame-cap">
          <span>Timeline</span>
          <span>reconstructed from cluster · nothing stored on disk</span>
        </div>
        <div className="frame-body">
          <Axis entries={entries} span={s} gaps={gaps} />
        </div>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "1.3fr 1fr", gap: "var(--s4)", marginTop: "var(--s4)" }}>
        <Changes entries={entries} />
        <Pods pods={view.pods} />
      </div>
    </>
  );
}

function Axis({
  entries, span: s, gaps,
}: {
  entries: TimelineEntry[];
  span: Span;
  gaps: { source: string; reason: string }[];
}) {
  return (
    <div className="tl">
      {LANES.map((lane) => {
        const mine = laneEntries(entries, lane);
        // A source that could not be read is named on its own lane. An empty
        // lane with no explanation reads as "this never happened".
        const gap = gaps.find((g) => g.source === gapSource(lane.key));
        return (
          <div className="tl-lane" key={lane.key}>
            <span className="lane-label">{lane.label}</span>
            <div className="tl-track">
              <div className="tl-rule" />
              {gap ? (
                <span style={{
                  position: "absolute", left: "var(--s3)", top: "50%",
                  transform: "translateY(-50%)", fontSize: 11, color: "var(--fg-3)",
                }}>
                  unavailable · {gap.reason}
                </span>
              ) : (
                mine.map((e, i) => (
                  <span
                    key={`${e.at}-${i}`}
                    className={`tl-mark ${lane.mark}`}
                    style={{ left: `${position(e.at, s)}%` }}
                    title={`${clock(e.at)} · ${e.title}${e.detail ? " · " + e.detail : ""}`}
                  />
                ))
              )}
            </div>
          </div>
        );
      })}
      <div className="tl-ticks">
        {ticks(s).map((t, i) => <span key={i}>{t}</span>)}
      </div>
    </div>
  );
}

// gapSource maps a lane to the source name the backend reports gaps under.
function gapSource(lane: string): string {
  return lane === "release" ? "helm" : lane === "deploy" ? "rollouts" : lane;
}

function Changes({ entries }: { entries: TimelineEntry[] }) {
  return (
    <div className="frame">
      <div className="frame-cap"><span>Changes</span><span>actor from managedFields</span></div>
      <div className="frame-body flush" style={{ padding: "var(--s3) var(--s4) var(--s4)" }}>
        {entries.length === 0 ? (
          <div style={{ color: "var(--fg-3)", fontSize: 12 }}>
            Nothing recoverable. The cluster keeps no record this far back.
          </div>
        ) : (
          <table className="tbl">
            <thead>
              <tr><th>When</th><th>Change</th><th>Actor</th></tr>
            </thead>
            <tbody>
              {entries.slice(0, 12).map((e, i) => (
                <tr key={`${e.at}-${i}`}>
                  <td className="mono">{clock(e.at)}</td>
                  <td style={{ whiteSpace: "normal" }}>
                    {e.title}
                    {e.detail && <span className="dim"> · {e.detail}</span>}
                  </td>
                  <td><ActorCell entry={e} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// A kubectl actor in prod is an out-of-band change. Highlighting it is the
// point: without it, finding the config change before the restarts is forensic
// work.
function ActorCell({ entry }: { entry: TimelineEntry }) {
  const a = entry.actor;
  if (!a?.name) return <span className="dim">—</span>;
  const outOfBand = a.kind === "kubectl";
  return (
    <span
      className={outOfBand ? undefined : "dim"}
      style={outOfBand ? { color: "var(--warn)" } : undefined}
      title={a.name}
    >
      {a.kind || a.name}
    </span>
  );
}

function Pods({ pods }: { pods: PodView[] }) {
  return (
    <div className="frame">
      <div className="frame-cap">
        <span>Pods</span>
        <span>{pods.length} {pods.length === 1 ? "replica" : "replicas"}</span>
      </div>
      <div className="frame-body flush" style={{ padding: "var(--s3) var(--s4) var(--s4)" }}>
        {pods.length === 0 ? (
          <div style={{ color: "var(--fg-3)", fontSize: 12 }}>No pods. This workload is not running anything.</div>
        ) : (
          <table className="tbl">
            <thead>
              <tr>
                <th style={{ width: 22 }}></th>
                <th>Name</th>
                <th className="r">Restarts</th>
                <th className="r">Age</th>
              </tr>
            </thead>
            <tbody>
              {pods.map((p) => (
                <tr key={p.name}>
                  <td><Glyph health={p.health} /></td>
                  <td className="name" title={p.reason || p.phase}>{p.name}</td>
                  <td className="r mono" style={p.restarts > 0 ? { color: "var(--err)" } : undefined}>
                    {p.restarts}
                  </td>
                  <td className="r mono">{p.ageSec ? age(p.ageSec) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function Stat({ label, value, title, warn }: { label: string; value: string; title?: string; warn?: boolean }) {
  return (
    <div className="frame" style={{ flex: 1 }}>
      <div className="frame-body" style={{ padding: "var(--s3) var(--s4)" }}>
        <div className="stat">{label}</div>
        <div className="stat-value" style={warn ? { color: "var(--warn)" } : undefined} title={title}>
          {value}
        </div>
      </div>
    </div>
  );
}

// shortImage keeps the tag, which is the part a developer reads, and drops the
// registry path that pushes it off the tile.
function shortImage(image?: string): string {
  if (!image) return "—";
  const last = image.split("/").pop() ?? image;
  const [name, tag] = last.split(":");
  return tag ? tag : name;
}

function secondsSince(iso: string): number {
  const t = Date.parse(iso);
  return Number.isFinite(t) ? (Date.now() - t) / 1000 : 0;
}

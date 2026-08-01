import { useEffect, useState } from "react";
import { api, type PromotionCell, type PromotionView } from "./api";
import { Glyph } from "./Status";
import { envToken } from "./health";
import { age } from "./detail";

// Screen 5. Is the fix in prod yet.
//
// The matrix compares and never promotes. No control here mutates anything,
// because deploying belongs to the pipeline that owns it, and a bulk action
// across environments is precisely the button nobody should have.

export function PromotionScreen({ onOpenApp }: { onOpenApp: (context: string, namespace: string, workload: string) => void }) {
  const [view, setView] = useState<PromotionView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api.promotion()
      .then((v) => { if (alive) setView(v); })
      .catch((e) => { if (alive) setErr(String(e.message ?? e)); });
    return () => { alive = false; };
  }, []);

  return (
    <>
      <div className="topbar">
        <div className="crumb">
          <span className="cur">Promotion</span>
          <span className="sep">·</span>
          <span className="up">all environments</span>
        </div>
        <span className="spacer" />
        {view && (
          <span style={{ color: "var(--fg-4)", fontSize: "var(--fs-label)" }}>
            {view.summary.apps} apps · {view.summary.drifted} with drift · {view.summary.ahead} ahead of upstream
          </span>
        )}
      </div>

      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Promotion</h1>
          <span className="page-sub">sorted by drift</span>
        </div>

        {err && (
          <div className="empty">
            <div className="head">Could not build the matrix</div>
            <div className="mono" style={{ fontSize: "var(--fs-data)" }}>{err}</div>
          </div>
        )}
        {!err && !view && (
          <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>reading every environment…</span></div>
        )}

        {view && view.unreachable && view.unreachable.length > 0 && (
          <div className="banner" style={{ marginBottom: "var(--s4)" }}>
            <span className="st st-unknown"><i className="glyph" /></span>
            <span>
              Not connected: <strong>{view.unreachable.join(", ")}</strong>. Those columns are unknown, not empty.
            </span>
          </div>
        )}

        {view && (
          <>
            <div className="frame">
              <div className="frame-body" style={{ padding: "var(--s4)" }}>
                <table className="matrix">
                  <thead>
                    <tr>
                      <th style={{ width: 200 }}>App</th>
                      <th style={{ width: 130 }}>Namespace</th>
                      {view.envs.map((e) => (
                        <th key={e.name} className="env-col" data-env={envToken({ risk: e.risk })}>
                          {e.name}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {view.rows.map((r) => (
                      <tr key={`${r.namespace}/${r.app}`}>
                        <td className="app">{r.app}</td>
                        <td style={{ color: "var(--fg-4)" }}>{r.namespace}</td>
                        {r.cells.map((c, i) => (
                          <td className="cell" key={c.env}>
                            <CellBody
                              cell={c}
                              onOpen={() => onOpenApp(view.envs[i]?.context ?? "", c.namespace ?? r.namespace, r.app)}
                            />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            <div style={{ display: "grid", gridTemplateColumns: "repeat(3, 1fr)", gap: "var(--s4)", marginTop: "var(--s4)" }}>
              <div className="note">
                <div className="note-title">Ahead is worse than behind</div>
                <p>Behind is a schedule: a fix lands in qa first. Ahead is a change nothing upstream made, so it
                takes the error hue rather than a warning.</p>
              </div>
              <div className="note">
                <div className="note-title">Same tag, different digest</div>
                <p>A mutable tag repushed means two environments show the same version and run different code.
                The digest comes from pod status, and a cell without one yet says pending rather than matching.</p>
              </div>
              <div className="note">
                <div className="note-title">Absent is not forbidden</div>
                <p>"Not deployed here" and "I cannot read this" are different facts. Conflating them during an
                incident is dangerous, so they render differently.</p>
              </div>
            </div>
          </>
        )}
      </div>
    </>
  );
}

function CellBody({ cell: c, onOpen }: { cell: PromotionCell; onOpen: () => void }) {
  if (c.state === "absent") {
    return (
      <>
        <span className="cell-none">—</span>
        <span className="cell-meta">not deployed</span>
      </>
    );
  }
  if (c.state === "denied") {
    return (
      <>
        <span className="cell-denied">no access</span>
        <span className="cell-meta">{c.note}</span>
      </>
    );
  }

  const colour = c.severe ? "var(--err)" : c.state === "behind" || c.state === "differs" ? "var(--warn)" : undefined;

  return (
    <>
      <span
        className="cell-tag"
        style={{ color: colour, cursor: "pointer" }}
        onClick={onOpen}
        title={c.image}
      >
        {c.tag || "—"}
      </span>
      <span className="cell-meta">
        {c.health && <Glyph health={c.health} />}
        {c.ready}
        {c.revisionAt && ` · ${age(secondsSince(c.revisionAt))}`}
      </span>
      {c.state !== "same" && (
        <span className="cell-meta">
          <span className={`drift-flag${c.severe ? " ahead" : ""}`} title={c.note}>{label(c.state)}</span>
        </span>
      )}
      {c.digestPending && c.state === "same" && (
        <span className="cell-meta" title="the digest comes from pod status and has not arrived">
          digest pending
        </span>
      )}
    </>
  );
}

function label(state: string): string {
  switch (state) {
    case "behind": return "behind upstream";
    case "ahead": return "ahead of upstream";
    case "differs": return "differs";
    case "digest-differs": return "digest differs";
  }
  return state;
}

function secondsSince(iso: string): number {
  const t = Date.parse(iso);
  return Number.isFinite(t) ? (Date.now() - t) / 1000 : 0;
}

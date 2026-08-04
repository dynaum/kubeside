import { useEffect, useState } from "react";
import { api, type FleetRow, type FleetView } from "./api";
import { Glyph } from "./Status";
import { routeHash } from "./route";
import { envToken, loudestEnv } from "./health";

// Screen 7. Is every cluster running the latest version.
//
// The promotion matrix compares environments side by side. This compares the
// clusters inside and across them, which is what a team running prod in three
// regions asks and the matrix cannot phrase.
//
// Five states, never conflated. A cluster behind a VPN is not a cluster
// without the app, and rendering both blank would answer the screen's own
// question with a guess.
//
// Environment colour is read off the row, never derived from its name. The
// backend classifies by keyword token and returns the name untouched, so
// prod-us-east and production are red while spelling no tier this screen could
// compare against. The chip prints the resolved name verbatim, the way every
// other screen does, and data-env carries the colour.

const NOT_PRESENT_LABEL: Record<string, string> = {
  unreachable: "no answer",
  denied: "no access",
  pending: "asking",
};

export function FleetScreen({ app, namespace }: { app: string; namespace: string }) {
  const [view, setView] = useState<FleetView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setView(null);
    setErr(null);
    api.fleet(app, namespace)
      .then((v) => { if (alive) setView(v); })
      .catch((e) => { if (alive) setErr(String(e.message ?? e)); });
    return () => { alive = false; };
  }, [app, namespace]);

  return (
    <>
      <div className="topbar">
        <div className="crumb">
          <span className="cur">{app}</span>
          <span className="sep">·</span>
          <span className="up">every cluster</span>
        </div>
        <span className="spacer" />
        <button className="btn" onClick={() => { window.location.hash = routeHash({ screen: "promotion" }); }}>
          Open in promotion
        </button>
      </div>

      <div className="page">
        <div className="page-head">
          <h1 className="page-title">{app}</h1>
          <span className="page-sub">
            {view
              ? `${view.clusters} clusters · ${view.present} running it · ${view.behind} behind${view.newest ? ` · newest is ${view.newest}` : ""}`
              : "asking every cluster"}
          </span>
        </div>

        {err && (
          <div className="empty">
            <div className="head">Could not ask the clusters</div>
            <div className="mono" style={{ fontSize: "var(--fs-data)" }}>{err}</div>
          </div>
        )}
        {!err && !view && (
          <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>asking every cluster…</span></div>
        )}

        {view?.mutableTag && (
          <div
            className="banner"
            data-env={loudestEnv(view.rows.filter((r) => r.mutableTag).map((r) => ({ color: r.envColor, risk: r.envRisk })))}
            style={{ marginBottom: "var(--s4)" }}
          >
            <span className="st st-err"><i className="glyph" /></span>
            <span>
              <strong>One tag resolves to more than one digest.</strong> These clusters claim the same
              version and are not running the same code.
            </span>
          </div>
        )}

        {view && view.clusters === 0 && (
          <div className="empty">
            <div className="head">No clusters to ask</div>
            <div style={{ color: "var(--fg-3)" }}>
              The kubeconfig names no contexts, so nothing was asked about {app}. A table of nothing would
              read as an answer, and there is none yet.
            </div>
          </div>
        )}

        {view && view.clusters > 0 && view.present === 0 && (
          <div className="empty" style={{ marginBottom: "var(--s4)" }}>
            <div className="head">Not found in any of {view.clusters} clusters</div>
            <div style={{ color: "var(--fg-3)" }}>
              The table below shows what each cluster answered. Check the name, or the namespace it lives in.
            </div>
          </div>
        )}

        {view && view.clusters > 0 && (
          <>
            <div className="frame">
              <div className="frame-body">
                <table className="tbl">
                  <thead>
                    <tr>
                      <th style={{ width: 180 }}>Cluster</th>
                      <th style={{ width: 84 }}>Env</th>
                      <th style={{ width: 120 }}>Version</th>
                      <th style={{ width: 104 }}>Health</th>
                      <th>Verdict</th>
                    </tr>
                  </thead>
                  <tbody>
                    {view.rows.map((r) => (
                      <tr key={r.context} data-env={envToken({ color: r.envColor, risk: r.envRisk })} className="env-edge">
                        <td className="name">
                          {r.context}
                          {r.aliases && r.aliases.length > 0 && (
                            <span className="cell-meta">also {r.aliases.join(", ")}</span>
                          )}
                          {/* The match is by identity, which strips the environment token, so
                              a row can report a different namespace from the one asked for.
                              Nothing else on the screen names it, and a substitution nobody
                              sees is a substitution nobody can challenge. */}
                          {r.namespace && r.namespace !== namespace && (
                            <span className="cell-meta">in {r.namespace}, not {namespace}</span>
                          )}
                        </td>
                        <td><span className="env-chip">{r.env}</span></td>
                        <VersionCell r={r} />
                        <td>
                          <HealthCell r={r} />
                        </td>
                        <td className="dim">
                          <Verdict r={r} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: "var(--s4)", marginTop: "var(--s4)" }}>
              <div className="note">
                <div className="note-title">Sorted by what needs attention</div>
                <p>A mutable tag sits at the top, and below it the less a cluster told us the higher it sits:
                the version you cannot see could be worse than the old one you can. A cluster the app is not
                deployed to stays with the healthy ones, because that is a schedule and not a disagreement.</p>
              </div>
              <div className="note">
                <div className="note-title">Four ways to have no version</div>
                <p>A cluster can carry no version for four different reasons: it did not answer, it refused to
                let us look, its image is pinned by digest, or the app simply is not deployed there. Rendering
                those the same would answer the screen's own question with a guess.</p>
              </div>
              <div className="note">
                <div className="note-title">The refusal names the verb</div>
                <p>A denied row carries the rule that would fix it, not just the word no. A developer can paste
                it into a Role request without a second round trip.</p>
              </div>
              <div className="note">
                <div className="note-title">Pending is not agreement</div>
                <DigestNote count={view.digestUnverified ?? 0} />
              </div>
            </div>
          </>
        )}
      </div>
    </>
  );
}

// A fleet with every digest resolved has no count to report, and printing
// "0 present rows still have no digest" states a problem nobody has. The rule
// still belongs on the screen, so it falls back to the rule itself.
function DigestNote({ count }: { count: number }) {
  if (count === 0) {
    return (
      <p>A present row whose digest never arrived counts as unverified rather than matching. A digest that
      never arrived cannot be evidence that two clusters agree.</p>
    );
  }
  const one = count === 1;
  return (
    <p>{count} present row{one ? "" : "s"} still {one ? "has" : "have"} no digest, so {one ? "it counts" : "they count"} as
    unverified rather than matching. A digest that never arrived cannot be evidence that two clusters
    agree.</p>
  );
}

function VersionCell({ r }: { r: FleetRow }) {
  if (r.state !== "present") {
    return <td><span className="cell-none">—</span></td>;
  }
  if (!r.tag) {
    return <td><span className="cell-denied">no tag</span></td>;
  }
  const color = r.mutableTag ? "var(--err)" : r.behind ? "var(--warn)" : undefined;
  return <td className="mono" style={{ color }}>{r.tag}</td>;
}

function HealthCell({ r }: { r: FleetRow }) {
  if (r.state === "present") {
    return (
      <>
        <Glyph health={r.health ?? "unknown"} />
        {r.ready && <Ratio ready={r.ready} />}
      </>
    );
  }
  if (r.state === "absent") {
    return <span className="cell-none">—</span>;
  }
  const label = NOT_PRESENT_LABEL[r.state] ?? r.state;
  if (r.state === "denied") {
    return <span className="cell-denied">{label}</span>;
  }
  return (
    <>
      <Glyph health="unknown" />
      <span className="cell-denied" style={{ marginLeft: 6 }}>{label}</span>
    </>
  );
}

function Ratio({ ready }: { ready: string }) {
  const i = ready.indexOf("/");
  if (i === -1) {
    return <span className="ratio" style={{ marginLeft: 6 }}>{ready}</span>;
  }
  return (
    <span className="ratio" style={{ marginLeft: 6 }}>
      {ready.slice(0, i)}
      <span className="of">{ready.slice(i)}</span>
    </span>
  );
}

function Verdict({ r }: { r: FleetRow }) {
  if (r.state !== "present") {
    return <>{r.note}</>;
  }
  const flagged = r.mutableTag || r.behind;
  return (
    <>
      {r.mutableTag && <span className="drift-flag ahead">mutable tag</span>}
      {!r.mutableTag && r.behind && <span className="drift-flag">behind</span>}
      {r.note && <span style={{ marginLeft: flagged ? 6 : 0 }}>{r.note}</span>}
      {r.digestPending && (
        <span className="tag tag-unknown" style={{ marginLeft: 6 }}>digest pending</span>
      )}
    </>
  );
}

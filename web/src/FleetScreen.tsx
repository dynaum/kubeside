import { useEffect, useState } from "react";
import { api, type FleetRow, type FleetView } from "./api";
import { Glyph } from "./Status";
import { routeHash } from "./route";
import type { EnvToken } from "./health";

// Screen 7. Is every cluster running the latest version.
//
// The promotion matrix compares environments side by side. This compares the
// clusters inside and across them, which is what a team running prod in three
// regions asks and the matrix cannot phrase.
//
// Five states, never conflated. A cluster behind a VPN is not a cluster
// without the app, and rendering both blank would answer the screen's own
// question with a guess.

const KNOWN_ENVS = new Set(["qa", "stg", "prod"]);

// Row.env is a resolved environment name, not necessarily one of the four
// design tokens: a context left unbound in the config file carries its own
// name (or the config's own label) here. Anything that is not one of the
// three classified tiers renders as unclassified, the same fallback the rest
// of the app gives an environment nobody named.
function fleetEnvToken(env: string): EnvToken {
  return KNOWN_ENVS.has(env) ? (env as EnvToken) : "unc";
}

function fleetEnvLabel(env: string): string {
  return KNOWN_ENVS.has(env) ? env : "unclassified";
}

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
          <div className="banner" style={{ marginBottom: "var(--s4)" }}>
            <span className="st st-err"><i className="glyph" /></span>
            <span>
              <strong>One tag resolves to more than one digest.</strong> These clusters claim the same
              version and are not running the same code.
            </span>
          </div>
        )}

        {view && view.present === 0 && (
          <div className="empty" style={{ marginBottom: "var(--s4)" }}>
            <div className="head">Not found in any of {view.clusters} clusters</div>
            <div style={{ color: "var(--fg-3)" }}>
              The table below shows what each cluster answered. Check the name, or the namespace it lives in.
            </div>
          </div>
        )}

        {view && (
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
                      <tr key={r.context} data-env={fleetEnvToken(r.env)} className="env-edge">
                        <td className="name">
                          {r.context}
                          {r.aliases && r.aliases.length > 0 && (
                            <span className="cell-meta">also {r.aliases.join(", ")}</span>
                          )}
                        </td>
                        <td><span className="env-chip">{fleetEnvLabel(r.env)}</span></td>
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
                <p>A mutable tag outranks everything, then a cluster that never answered, then one that is
                behind, then one we are not allowed to read, then one present without a tag we can name, then
                one still asking. An unread cluster beats a known-old one: the version you cannot see could be
                worse than the one you can.</p>
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
                <p>{view.digestUnverified ?? 0} present row{(view.digestUnverified ?? 0) === 1 ? "" : "s"} still{" "}
                {(view.digestUnverified ?? 0) === 1 ? "has" : "have"} no digest, so {(view.digestUnverified ?? 0) === 1 ? "it counts" : "they count"} as
                unverified rather than matching. A digest that never arrived cannot be evidence that two
                clusters agree.</p>
              </div>
            </div>
          </>
        )}
      </div>
    </>
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

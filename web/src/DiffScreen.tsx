import { useEffect, useState } from "react";
import { api, type ContextView, type CrossRow, type DiffView } from "./api";
import { envToken } from "./health";

// Screen 3b. What differs between two environments, and which differences
// matter.
//
// "These 34 keys differ" is not an answer: half of them are supposed to. Every
// row is classified and every classification names the rule behind it, because
// a verdict nobody can audit is a verdict nobody should trust.

export function DiffScreen({
  context, contexts, namespace, workload, other, onBack, onPickOther,
}: {
  context: ContextView;
  contexts: ContextView[];
  namespace: string;
  workload: string;
  other: string;
  onBack: () => void;
  onPickOther: (name: string) => void;
}) {
  const [view, setView] = useState<DiffView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setView(null);
    setErr(null);
    if (!other) return;
    let alive = true;
    api.diff(context.name, namespace, workload, other)
      .then((v) => { if (alive) setView(v); })
      .catch((e) => { if (alive) setErr(String(e.message ?? e)); });
    return () => { alive = false; };
  }, [context.name, namespace, workload, other]);

  const others = contexts.filter((c) => c.name !== context.name);

  return (
    <>
      <div className={`topbar env-edge${context.hazard ? " hazard" : ""}`} data-env={envToken(context)}>
        <span className="env-chip">{context.environment}</span>
        <div className="crumb">
          <button className="tab" style={{ padding: 0 }} onClick={onBack}>apps</button>
          <span className="sep">/</span>
          <span className="up">{namespace}</span>
          <span className="sep">/</span>
          <span className="cur">{workload}</span>
          <span className="sep">·</span>
          <span className="up">compare</span>
        </div>
        <span className="spacer" />
        {view && (
          <span style={{ color: "var(--fg-4)", fontSize: "var(--fs-label)" }}>
            {view.summary.drift} drift · {view.summary.suspicious} suspicious · {view.summary.missing} missing ·{" "}
            {view.summary.expected} expected
          </span>
        )}
      </div>

      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Configuration diff</h1>
          {view && <span className="page-sub">{workload} · container {view.container}</span>}
          <span className="spacer" />
          <span className="env-chip" data-env={envToken(context)}>{context.environment}</span>
          <span style={{ color: "var(--fg-4)" }}>→</span>
          {others.length === 0 ? (
            <span className="page-sub">no other context in this kubeconfig</span>
          ) : (
            <div className="row" style={{ gap: "var(--s2)" }}>
              {others.map((c) => (
                <button
                  key={c.name}
                  className={c.name === other ? "btn btn-primary" : "btn"}
                  onClick={() => onPickOther(c.name)}
                >
                  {c.environment || c.name}
                </button>
              ))}
            </div>
          )}
        </div>

        {!other && others.length > 0 && (
          <div className="empty">
            <div className="head">Pick an environment to compare against</div>
          </div>
        )}
        {err && (
          <div className="empty">
            <div className="head">Could not compare</div>
            <div className="mono" style={{ fontSize: "var(--fs-data)" }}>{err}</div>
          </div>
        )}
        {other && !err && !view && (
          <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>resolving both sides…</span></div>
        )}

        {view && (
          <>
            <div className="frame">
              <div className="frame-body" style={{ padding: "var(--s4)" }}>
                <table className="kv">
                  <thead>
                    <tr>
                      <th style={{ width: 230 }}>Key</th>
                      <th>{view.left.env.name || view.left.context}</th>
                      <th>{view.right.env.name || view.right.context}</th>
                      <th style={{ width: 140 }}>Classification</th>
                    </tr>
                  </thead>
                  <tbody>
                    {view.rows.map((r) => <Row key={r.key} row={r} />)}
                  </tbody>
                </table>
              </div>
            </div>

            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: "var(--s4)", marginTop: "var(--s4)" }}>
              <div className="note">
                <div className="note-title">Drift</div>
                <p>Keys that differ with nothing in the values to explain why. An address that names its own
                environment is expected to differ; a feature flag is not.</p>
              </div>
              <div className="note">
                <div className="note-title">Suspicious</div>
                <p>Keys that match where matching is the problem: a development setting that survived into a
                riskier environment, capacity carried over unchanged, or a value pointing at the other
                environment.</p>
              </div>
              <div className="note">
                <div className="note-title">Secrets never compared by value</div>
                <p>Diffed by digest only. The tool reports that two credentials differ without ever placing
                production credentials beside staging credentials on one screen.</p>
              </div>
            </div>
          </>
        )}
      </div>
    </>
  );
}

function Row({ row: r }: { row: CrossRow }) {
  return (
    <tr>
      <td className="k">{r.key}</td>
      <td className="v"><Side value={r.left} unset={r.leftUnset} masked={r.masked} digest={r.leftDigest} /></td>
      <td className="v" style={colorFor(r)}>
        <Side value={r.right} unset={r.rightUnset} masked={r.masked} digest={r.rightDigest} />
      </td>
      <td>
        <span className={`tag ${tagFor(r.class)}`} title={r.reason}>{r.class || "not compared"}</span>
      </td>
    </tr>
  );
}

function Side({
  value, unset, masked, digest,
}: {
  value?: string;
  unset?: boolean;
  masked?: boolean;
  digest?: string;
}) {
  // Not set and set to empty are different configurations, and conflating them
  // hides the difference that breaks a promotion.
  if (unset) return <span style={{ color: "var(--err)" }}>&lt;unset&gt;</span>;
  if (masked) {
    return (
      <>
        <span className="masked">••••••••</span>
        <span style={{ color: "var(--fg-4)", fontSize: "var(--fs-label)", marginLeft: 6 }}>
          {digest ? digest : "no digest · not readable"}
        </span>
      </>
    );
  }
  if (value === "") return <span className="dim">empty</span>;
  return <>{value}</>;
}

function tagFor(cls: string): string {
  switch (cls) {
    case "drift": return "tag-drift";
    case "suspicious": return "tag-drift";
    case "missing": return "tag-missing";
    case "expected": return "tag-expected";
    case "match": return "tag-expected";
  }
  return "tag-unknown";
}

function colorFor(r: CrossRow): React.CSSProperties | undefined {
  if (r.class === "drift" || r.class === "suspicious") return { color: "var(--warn)" };
  if (r.class === "missing") return { color: "var(--err)" };
  return undefined;
}

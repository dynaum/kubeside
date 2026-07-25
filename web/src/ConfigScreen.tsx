import { useEffect, useState } from "react";
import {
  api,
  type ConfigView, type ContextView, type ResolvedContainer, type ResolvedDiff, type ResolvedValue,
} from "./api";
import { envToken } from "./health";

// Screen 3. The configuration the container actually received.
//
// Kubernetes spreads that answer across env, envFrom, valueFrom, the downward
// API, and mounted volumes, with precedence between them. This screen applies
// the same precedence the kubelet did and says where every value came from,
// because knowing MAX_CONNECTIONS is 25 matters far less than knowing which
// ConfigMap to edit.

export function ConfigScreen({
  context, namespace, workload, onBack, onNavigate,
}: {
  context: ContextView;
  namespace: string;
  workload: string;
  onBack: () => void;
  onNavigate: (screen: "app" | "logs" | "diff") => void;
}) {
  const [view, setView] = useState<ConfigView | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [selected, setSelected] = useState(0);
  // Revealed values live only in this component's state. They are never merged
  // back into the view, so a re-read of the config returns to masked.
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const [revealErr, setRevealErr] = useState<Record<string, string>>({});

  useEffect(() => {
    setView(null);
    setErr(null);
    setSelected(0);
    setRevealed({});
    setRevealErr({});
    let alive = true;
    api.config(context.name, namespace, workload)
      .then((v) => { if (alive) setView(v); })
      .catch((e) => { if (alive) setErr(String(e)); });
    return () => { alive = false; };
  }, [context.name, namespace, workload]);

  const container = view?.containers[selected];

  const reveal = (v: ResolvedValue) => {
    const at = `${container?.name}|${v.key}`;
    api.reveal(context.name, namespace, v.source.ref ?? "", v.source.key || v.key, workload)
      .then((r) => {
        setRevealed((prev) => ({
          ...prev,
          [at]: r.binary ? (r.note ?? "binary data") : (r.value ?? ""),
        }));
      })
      .catch((e) => setRevealErr((prev) => ({ ...prev, [at]: String(e.message ?? e) })));
  };

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
        </div>
        <span className="spacer" />
        {view?.pod && <span className="page-sub mono" style={{ fontSize: 11 }}>read from {view.pod}</span>}
        <button className="btn" onClick={() => onNavigate("diff")}>Compare environment</button>
      </div>

      <div className="page">
        <div className="tabs">
          <button className="tab" onClick={() => onNavigate("app")}>Timeline</button>
          <span className="tab sel">Configuration</span>
          <button className="tab" onClick={() => onNavigate("logs")}>Logs</button>
        </div>

        {err && (
          <div className="empty">
            <div className="head">Could not resolve configuration</div>
            <div className="mono" style={{ fontSize: 12 }}>{err}</div>
          </div>
        )}
        {!err && !view && (
          <div><span className="spinner" /> <span style={{ color: "var(--fg-3)" }}>resolving configuration…</span></div>
        )}

        {view && container && (
          <>
            <div className="row" style={{ marginBottom: "var(--s4)", gap: "var(--s2)" }}>
              <span className="stat" style={{ marginRight: "var(--s2)" }}>Container</span>
              {view.containers.map((c, i) => (
                <button
                  key={c.name}
                  className={i === selected ? "btn btn-primary" : "btn"}
                  onClick={() => setSelected(i)}
                >
                  {c.init ? `init: ${c.name}` : c.name}
                </button>
              ))}
              <span className="spacer" />
              <span style={{ color: "var(--fg-4)", fontSize: 11 }}>
                {container.values.length} keys from {sourceCount(container)} {sourceCount(container) === 1 ? "source" : "sources"}
              </span>
            </div>

            <div className="frame">
              <div className="frame-cap">
                <span>Effective environment · container {container.name}</span>
                <span>
                  {view.comparedTo
                    ? `values the container received · compared with revision ${view.comparedTo}`
                    : "values the container actually received"}
                </span>
              </div>
              <div className="frame-body" style={{ padding: "var(--s4)" }}>
                <Table
                  container={container}
                  reveal={reveal}
                  revealed={revealed}
                  revealErr={revealErr}
                />
              </div>
            </div>

            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "var(--s4)", marginTop: "var(--s4)" }}>
              <div className="note">
                <div className="note-title">What this reading can and cannot promise</div>
                <p>{view.caveat}</p>
                <p className="fix" style={{ color: "var(--fg-2)" }}>
                  Values come from a running pod rather than the workload's template. The template is what was
                  asked for; the pod is what happened, and during a rollout those differ.
                </p>
              </div>
              <div className="note">
                <div className="note-title">Secret handling</div>
                <p>
                  Secret values are masked by never being fetched. Masking a value kubeside already holds is a
                  rendering decision; masking one it never read is a property.
                </p>
                <p className="fix" style={{ color: "var(--fg-2)" }}>
                  Reveal is disabled until it is gated on <code className="mono">get</code> for that specific
                  Secret. The control stays visible and names what it needs.
                </p>
              </div>
            </div>
          </>
        )}

        {view && !container && (
          <div className="empty">
            <div className="head">No containers</div>
            <div>This pod reports no containers, which should not happen.</div>
          </div>
        )}
      </div>
    </>
  );
}

function Table({
  container, reveal, revealed, revealErr,
}: {
  container: ResolvedContainer;
  reveal: (v: ResolvedValue) => void;
  revealed: Record<string, string>;
  revealErr: Record<string, string>;
}) {
  return (
    <table className="kv">
      <thead>
        <tr>
          <th style={{ width: 250 }}>Key</th>
          <th>Effective value</th>
          <th style={{ width: 240 }}>Source</th>
          <th style={{ width: 190 }}>Since last revision</th>
        </tr>
      </thead>
      <tbody>
        {container.values.map((v) => (
          <tr key={v.key}>
            <td className="k">{v.key}</td>
            <td className="v">
              <ValueCell
                value={v}
                revealed={revealed[`${container.name}|${v.key}`]}
                error={revealErr[`${container.name}|${v.key}`]}
                onReveal={() => reveal(v)}
              />
            </td>
            <td className="src"><SourceCell value={v} /></td>
            <td className="src"><DiffCell diff={v.diff} /></td>
          </tr>
        ))}
        {container.mounts?.map((m) => (
          <tr key={m.path}>
            <td className="k">{m.path}</td>
            <td className="v">
              {m.masked ? <span className="masked">••••••••</span> : <span className="dim">mounted files</span>}
            </td>
            <td className="src">
              volume <span style={{ color: "var(--fg-3)" }}>{m.source.kind} {m.source.ref}</span>
              {m.readOnly && " · read-only"}
            </td>
            <td className="src">
              <span className="tag tag-unknown" title="mounted file contents are not versioned">not recoverable</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ValueCell({
  value: v, revealed, error, onReveal,
}: {
  value: ResolvedValue;
  revealed?: string;
  error?: string;
  onReveal: () => void;
}) {
  if (v.masked) {
    if (revealed !== undefined) {
      return (
        <>
          {revealed}
          {/* The reveal is on the session timeline. Saying so is part of the
              deal: reading a credential leaves a trace. */}
          <span className="tag tag-drift" style={{ marginLeft: 6 }} title="this reveal was recorded on the timeline">
            revealed
          </span>
        </>
      );
    }
    if (error) {
      return (
        <>
          <span className="masked">••••••••••••</span>
          <span className="dim" style={{ marginLeft: 6, fontSize: 11 }}>{error}</span>
        </>
      );
    }
    return (
      <>
        <span className="masked">••••••••••••</span>
        {/* Disabled, never hidden: the control names what it needs. */}
        <span className="rbac" style={{ marginLeft: "var(--s2)" }}>
          <button
            className="btn"
            style={{ padding: "1px 6px" }}
            disabled={!v.canReveal}
            title={v.canReveal ? "fetches this key and records the reveal on the timeline" : v.revealReason}
            onClick={onReveal}
          >
            Reveal
          </button>
        </span>
      </>
    );
  }
  if (v.missing) {
    // A key that is not there is frequently why a container is crashing. It
    // must never render as an empty value.
    return (
      <>
        <span className="tag tag-missing">missing</span>
        <span className="dim" style={{ marginLeft: 6, fontSize: 11 }}>{v.reason}</span>
      </>
    );
  }
  if (v.diff?.state === "removed") return <span className="dim">no longer set</span>;
  if (v.value === "") return <span className="dim">empty</span>;
  return <>{v.value}</>;
}

// The honesty column. Inline values survive per revision inside old
// ReplicaSets; ConfigMap and Secret contents do not, and those cells say so
// rather than claiming a value held steady.
function DiffCell({ diff }: { diff?: ResolvedDiff }) {
  const state = diff?.state ?? "";
  switch (state) {
    case "changed":
      return (
        <span className="tag tag-drift" title={`was ${diff?.previous}`}>
          was {truncate(diff?.previous ?? "")}
        </span>
      );
    case "source-changed":
      return (
        <span className="tag tag-drift" title={`was ${diff?.previous}`}>
          source was {truncate(diff?.previous ?? "")}
        </span>
      );
    case "added":
      return <span className="tag tag-expected">added</span>;
    case "removed":
      return (
        <span className="tag tag-missing" title={diff?.reason}>
          removed
        </span>
      );
    case "not-recoverable":
      return (
        <span className="tag tag-unknown" title={diff?.reason}>
          not recoverable
        </span>
      );
    case "runtime":
      return <span className="dim" title={diff?.reason}>runtime</span>;
    case "unchanged":
      return <span className="dim">unchanged</span>;
  }
  return <span className="dim">—</span>;
}

function truncate(s: string): string {
  return s.length > 18 ? s.slice(0, 17) + "…" : s;
}

function SourceCell({ value: v }: { value: ResolvedValue }) {
  const s = v.source;
  return (
    <>
      {label(s.kind)}
      {s.ref && <span style={{ color: "var(--fg-3)" }}> {s.ref}</span>}
      {s.key && s.key !== v.key && <span> · key {s.key}</span>}
      {v.overrides && (
        <span className="tag tag-drift" style={{ marginLeft: 6 }} title="this value shadowed another source">
          overrides
        </span>
      )}
    </>
  );
}

function label(kind: string): string {
  switch (kind) {
    case "inline": return "env inline";
    case "configMap": return "configMap";
    case "secret": return "secret";
    case "downwardAPI": return "downward API";
    case "resourceField": return "resource field";
  }
  return kind;
}

// sourceCount is how many distinct places this container's config came from,
// which is the number that tells you how scattered it is.
function sourceCount(c: ResolvedContainer): number {
  const seen = new Set(c.values.map((v) => `${v.source.kind}/${v.source.ref ?? ""}`));
  return seen.size;
}

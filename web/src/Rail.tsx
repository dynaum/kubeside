import type { ContextView } from "./api";
import { envToken } from "./health";

const VERSION = "dev";

export function Rail({
  contexts,
  selected,
  onSelect,
  onPromotion,
  promotionSelected,
}: {
  contexts: ContextView[];
  selected: string | null;
  onSelect: (name: string) => void;
  onPromotion: () => void;
  promotionSelected: boolean;
}) {
  return (
    <aside className="rail">
      <div className="rail-brand">
        <span className="mark">kubeside</span>
        <span className="ver">{VERSION}</span>
      </div>
      <div className="rail-sect">Environments</div>
      {contexts.map((c) => (
        <button
          key={c.name}
          className={`rail-item${c.name === selected ? " sel" : ""}${c.hazard ? " hazard" : ""}`}
          data-env={envToken(c)}
          onClick={() => onSelect(c.name)}
          title={`${c.environment} · ${c.risk} risk · writes ${c.write}`}
        >
          <span>{c.name}</span>
          <ConnState c={c} />
        </button>
      ))}
      <div className="rail-sect">Views</div>
      <button
        className={`rail-item${promotionSelected ? " sel" : ""}`}
        onClick={onPromotion}
        title="is the fix in prod yet"
      >
        <span>Promotion</span>
      </button>

      <div style={{ marginTop: "auto", padding: "var(--s4)", borderTop: "1px solid var(--line)" }}>
        <div className="mono" style={{ fontSize: "var(--fs-col)", color: "var(--fg-4)", lineHeight: 1.6 }}>
          reads ~/.kube/config
          <br />
          writes nothing to disk
        </div>
      </div>
    </aside>
  );
}

function ConnState({ c }: { c: ContextView }) {
  if (c.state === "live") return <span className="count" style={{ color: "var(--ok)" }}>live</span>;
  if (c.state === "connecting") return <span className="spinner" />;
  if (c.state === "stale") return <span className="count">stale</span>;
  if (c.state === "unauthorized") return <span className="count" style={{ color: "var(--warn)" }}>auth</span>;
  if (c.state === "unreachable") return <span className="count" style={{ color: "var(--fg-4)" }}>offline</span>;
  return <span className="count">—</span>;
}

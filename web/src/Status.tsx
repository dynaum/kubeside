import { healthClass } from "./health";

export function Glyph({ health }: { health: string }) {
  return (
    <span className={`st ${healthClass(health)}`} title={health}>
      <i className="glyph" />
    </span>
  );
}

export function StatusLabel({ health }: { health: string }) {
  return (
    <span className={`st ${healthClass(health)}`}>
      <i className="glyph" />
      <span className="label">{health}</span>
    </span>
  );
}

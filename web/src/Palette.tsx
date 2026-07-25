import { useEffect, useMemo, useRef, useState } from "react";
import { api, type AppView, type ContextView } from "./api";
import { commands, groups, move, search, split, type Command } from "./commands";
import type { Route } from "./route";
import { healthClass } from "./health";

// Screen 6. Every navigation and action reachable from the keyboard.
//
// Somebody arriving from k9s judges a tool on this within thirty seconds, so
// the palette is not a search box bolted onto a mouse-driven UI: it is the
// fastest way to reach anything the product does.

export function Palette({
  open, contexts, current, route, onClose, onRun,
}: {
  open: boolean;
  contexts: ContextView[];
  current: ContextView | null;
  route: Route;
  onClose: () => void;
  onRun: (route: Route) => void;
}) {
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const [apps, setApps] = useState<AppView[]>([]);
  const input = useRef<HTMLInputElement>(null);

  // The app list is read when the palette opens rather than held all along, so
  // a palette nobody opened costs nothing.
  useEffect(() => {
    if (!open || !current) return;
    let alive = true;
    api.apps(current.name)
      .then((v) => { if (alive) setApps(v.apps); })
      .catch(() => {});
    return () => { alive = false; };
  }, [open, current?.name]);

  useEffect(() => {
    if (open) {
      setQuery("");
      setIndex(0);
      input.current?.focus();
    }
  }, [open]);

  const all = useMemo(
    () => commands(contexts, current, apps, route),
    [contexts, current, apps, route],
  );
  const matches = useMemo(() => search(all, query), [all, query]);
  const selected = matches[Math.min(index, Math.max(matches.length - 1, 0))];

  if (!open) return null;

  const run = (c?: Command) => {
    if (!c) return;
    onRun(c.route);
    onClose();
  };

  const onKey = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        onClose();
        break;
      case "ArrowDown":
        e.preventDefault();
        setIndex((i) => move(i, 1, matches.length));
        break;
      case "ArrowUp":
        e.preventDefault();
        setIndex((i) => move(i, -1, matches.length));
        break;
      case "Enter":
        e.preventDefault();
        run(selected?.command);
        break;
    }
  };

  let position = -1;

  return (
    <div
      style={{
        position: "absolute", inset: 0, background: "rgba(4,7,8,.66)",
        display: "flex", alignItems: "flex-start", justifyContent: "center",
        paddingTop: 120, zIndex: 50,
      }}
      onClick={onClose}
    >
      <div className="palette" onClick={(e) => e.stopPropagation()}>
        <div className="palette-input">
          <span style={{ color: "var(--fg-4)" }}>›</span>
          <input
            ref={input}
            value={query}
            onChange={(e) => { setQuery(e.target.value); setIndex(0); }}
            onKeyDown={onKey}
            placeholder="jump to an app, or run something"
            style={{
              flex: 1, background: "none", border: "none", outline: "none",
              color: "var(--fg)", font: "inherit",
            }}
          />
          <span className="hint" style={{ color: "var(--fg-4)", fontSize: 11 }}>
            {matches.length} {matches.length === 1 ? "result" : "results"}
          </span>
        </div>

        <div style={{ maxHeight: 420, overflow: "auto" }}>
          {matches.length === 0 && (
            <div className="palette-row" style={{ color: "var(--fg-4)" }}>
              nothing matches. Only environments you have opened are searched.
            </div>
          )}

          {groups(matches).map((g) => (
            <div key={g.name}>
              <div className="palette-group">{g.name}</div>
              {g.matches.map((m) => {
                position++;
                const at = position;
                return (
                  <div
                    key={m.command.id}
                    className={`palette-row${at === index ? " sel" : ""}`}
                    onMouseEnter={() => setIndex(at)}
                    onClick={() => run(m.command)}
                  >
                    {m.command.health ? (
                      <span className={`st ${healthClass(m.command.health)}`}><i className="glyph" /></span>
                    ) : (
                      <span style={{ color: "var(--fg-4)" }}>⇥</span>
                    )}
                    <span className="path">
                      {split(m.command.label, m.ranges).map((p, i) =>
                        p.hit ? <strong key={i} style={{ fontWeight: 600 }}>{p.text}</strong> : <span key={i}>{p.text}</span>,
                      )}
                    </span>
                    {m.command.hint && <span className="hint">{m.command.hint}</span>}
                  </div>
                );
              })}
            </div>
          ))}
        </div>

        <div style={{
          display: "flex", gap: "var(--s4)", padding: "var(--s2) var(--s4)",
          borderTop: "1px solid var(--line)", background: "var(--surface)",
          color: "var(--fg-4)", fontSize: 10,
        }}>
          <span><span className="kbd">↑</span><span className="kbd">↓</span> navigate</span>
          <span><span className="kbd">↵</span> open</span>
          <span style={{ marginLeft: "auto" }}><span className="kbd">esc</span> dismiss</span>
        </div>
      </div>
    </div>
  );
}

import { useEffect, useMemo, useRef, useState } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { api, type AppView, type ContextView } from "./api";
import { commands, groups, move, search, split, type Action, type Command } from "./commands";
import type { Route } from "./route";
import { healthClass } from "./health";

// Screen 6. Every navigation and action reachable from the keyboard.
//
// Somebody arriving from k9s judges a tool on this within thirty seconds, so
// the palette is not a search box bolted onto a mouse-driven UI: it is the
// fastest way to reach anything the product does.
//
// The shell is a Radix dialog for its behaviour only: focus trapped while it is
// open, restored to whatever opened it, Escape handled, and the rest of the
// page hidden from assistive technology. Every pixel is still tokens.css. The
// list is a combobox over a listbox, so arrowing through it announces the
// command rather than nothing at all.

export function Palette({
  open, contexts, current, route, onClose, onRun, onAction,
}: {
  open: boolean;
  contexts: ContextView[];
  current: ContextView | null;
  route: Route;
  onClose: () => void;
  onRun: (route: Route) => void;
  onAction: (action: Action) => void;
}) {
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const [apps, setApps] = useState<AppView[]>([]);
  const input = useRef<HTMLInputElement>(null);
  const opener = useRef<HTMLElement | null>(null);

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
    }
  }, [open]);

  const all = useMemo(
    () => commands(contexts, current, apps, route),
    [contexts, current, apps, route],
  );
  const matches = useMemo(() => search(all, query), [all, query]);
  const selected = matches[Math.min(index, Math.max(matches.length - 1, 0))];

  // A command either goes somewhere or changes something. Settings close the
  // palette like everything else: seeing the result is the confirmation, and a
  // panel that stayed open would be a settings screen by accident.
  const run = (c?: Command) => {
    if (!c) return;
    if (c.action) onAction(c.action);
    else if (c.route) onRun(c.route);
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

  const optionId = (at: number) => `palette-option-${at}`;
  const active = matches.length > 0 ? optionId(Math.min(index, matches.length - 1)) : undefined;

  return (
    <Dialog.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <Dialog.Portal>
        <Dialog.Overlay
          style={{
            position: "absolute", inset: 0, background: "rgba(4,7,8,.66)",
            display: "flex", alignItems: "flex-start", justifyContent: "center",
            paddingTop: 120, zIndex: 50,
          }}
        />
        <Dialog.Content
          className="palette"
          aria-label="Command palette"
          aria-describedby={undefined}
          style={{
            position: "absolute", top: 120, left: "50%", transform: "translateX(-50%)",
            zIndex: 51,
          }}
          onOpenAutoFocus={(e) => {
            // Radix would focus the content; the input is what somebody types
            // into, and landing anywhere else costs a keystroke every time.
            // This fires before focus moves, so it is also where the element to
            // return to is captured.
            e.preventDefault();
            opener.current = document.activeElement as HTMLElement | null;
            input.current?.focus();
          }}
          onCloseAutoFocus={(e) => {
            // Back to whatever the developer was on. Dumped on <body>, their
            // next Tab restarts from the top of the page.
            e.preventDefault();
            opener.current?.focus();
          }}
        >
          <Dialog.Title style={{ position: "absolute", width: 1, height: 1, overflow: "hidden", clip: "rect(0 0 0 0)" }}>
            Command palette
          </Dialog.Title>
        <div className="palette-input">
          <span style={{ color: "var(--fg-4)" }}>›</span>
          <input
            ref={input}
            value={query}
            onChange={(e) => { setQuery(e.target.value); setIndex(0); }}
            onKeyDown={onKey}
            placeholder="jump to an app, or run something"
            role="combobox"
            aria-expanded
            aria-controls="palette-results"
            aria-activedescendant={active}
            aria-autocomplete="list"
            style={{
              flex: 1, background: "none", border: "none", outline: "none",
              color: "var(--fg)", font: "inherit",
            }}
          />
          <span className="hint" style={{ color: "var(--fg-4)", fontSize: "var(--fs-label)" }}>
            {matches.length} {matches.length === 1 ? "result" : "results"}
          </span>
        </div>

        <div id="palette-results" role="listbox" aria-label="Commands" style={{ maxHeight: 420, overflow: "auto" }}>
          {matches.length === 0 && (
            <div className="palette-row" style={{ color: "var(--fg-4)" }}>
              nothing matches. Only environments you have opened are searched.
            </div>
          )}

          {groups(matches).map((g) => (
            <div key={g.name} role="group" aria-label={g.name}>
              <div className="palette-group">{g.name}</div>
              {g.matches.map((m) => {
                position++;
                const at = position;
                return (
                  <div
                    key={m.command.id}
                    id={optionId(at)}
                    role="option"
                    aria-selected={at === index}
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
          color: "var(--fg-4)", fontSize: "var(--fs-col)",
        }}>
          <span><span className="kbd">↑</span><span className="kbd">↓</span> navigate</span>
          <span><span className="kbd">↵</span> open</span>
          <span style={{ marginLeft: "auto" }}><span className="kbd">esc</span> dismiss</span>
        </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

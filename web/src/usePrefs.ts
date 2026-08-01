import { useCallback, useEffect, useRef, useState } from "react";
import type { Action } from "./commands";
import { DEFAULT, apply, cycleTheme, read, step, write, type Prefs } from "./prefs";

// The browser side of prefs.ts: read once, apply on change, remember.
//
// All the decisions live in prefs.ts, which is pure. This holds the wiring that
// needs a window: the media query, the shortcuts, and localStorage itself.

/** Private browsing can throw on the property access, not just on the call. */
function storage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

const DARK = "(prefers-color-scheme: dark)";

export function usePrefs(): { prefs: Prefs; run: (action: Action) => void } {
  const [prefs, setPrefs] = useState<Prefs>(() => {
    try {
      return read(storage());
    } catch {
      return { ...DEFAULT };
    }
  });
  const [prefersDark, setPrefersDark] = useState(
    () => window.matchMedia?.(DARK).matches ?? true,
  );

  // "system" keeps deferring rather than resolving once, so a desktop that
  // flips at sunset takes kubeside with it.
  useEffect(() => {
    const mql = window.matchMedia?.(DARK);
    if (!mql) return;
    const onChange = (e: MediaQueryListEvent) => setPrefersDark(e.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  useEffect(() => {
    apply(document.documentElement, prefs, prefersDark);
  }, [prefs, prefersDark]);

  const run = useCallback((action: Action) => {
    setPrefs((p) => {
      let next: Prefs;
      switch (action) {
        case "theme:cycle": next = { ...p, theme: cycleTheme(p.theme) }; break;
        case "scale:up": next = { ...p, scale: step(p.scale, 1) }; break;
        case "scale:down": next = { ...p, scale: step(p.scale, -1) }; break;
        case "scale:reset": next = { ...p, scale: DEFAULT.scale }; break;
      }
      write(storage(), next);
      return next;
    });
  }, []);

  useShortcuts(run);
  return { prefs, run };
}

// Deliberately not cmd+plus and cmd+minus. Those are browser zoom, which the
// developer already has and which scales the spacing grid along with the text.
// This scale holds the grid and moves only type and row height, so the two
// compose: zoom the window, size the type inside it. Taking the zoom bindings
// would replace a control with a similar one and break that.
function useShortcuts(run: (action: Action) => void) {
  const latest = useRef(run);
  latest.current = run;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const cmd = e.metaKey || e.ctrlKey;
      if (!cmd) return;

      if (e.shiftKey && !e.altKey && e.key.toLowerCase() === "l") {
        e.preventDefault();
        latest.current("theme:cycle");
        return;
      }
      if (!e.altKey) return;

      // Option changes the character on macOS, so the physical key is what to
      // read. Equal and Minus are the unshifted names of the +/- keys.
      switch (e.code) {
        case "Equal":
        case "NumpadAdd":
          e.preventDefault();
          latest.current("scale:up");
          break;
        case "Minus":
        case "NumpadSubtract":
          e.preventDefault();
          latest.current("scale:down");
          break;
        case "Digit0":
        case "Numpad0":
          e.preventDefault();
          latest.current("scale:reset");
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}

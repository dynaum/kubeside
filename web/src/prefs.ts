/**
 * The two things a developer can change about how kubeside looks.
 *
 * Both live in the browser. The binary still writes nothing to disk: the
 * guarantee in docs/04-multi-cluster.md covers observed data, and a preference
 * somebody chose is the same kind of thing as the config file they chose to
 * create. It never leaves their machine either way.
 *
 * Pure, like rows.ts and commands.ts. Nothing here touches a global, so the
 * whole module is testable without a DOM.
 */

export type Theme = "system" | "dark" | "light";

export interface Prefs {
  theme: Theme;
  scale: number;
}

/**
 * Six rungs. Discrete rather than continuous so a keypress has a predictable
 * feel and nobody lands on 1.037.
 *
 * The top of the range is a genuinely different product shape: the design
 * system trades size for rows on purpose, and 1.6 spends that trade the other
 * way. It exists because reading the screen at all beats reading more of it.
 */
export const SCALES = [0.9, 1, 1.1, 1.25, 1.4, 1.6];

export const DEFAULT: Prefs = { theme: "system", scale: 1 };

const KEY = "kubeside.prefs";

/** The subset of localStorage this needs, so a test can supply its own. */
export interface Store {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

/**
 * read never throws and never returns something unusable.
 *
 * Storage is shared with anything else served from 127.0.0.1, private browsing
 * throws on access rather than returning null, and a hand-edited value can hold
 * anything. A preference that took the UI down with it would be a poor trade
 * for remembering a font size.
 */
export function read(storage: Store | null): Prefs {
  let raw: string | null = null;
  try {
    raw = storage?.getItem(KEY) ?? null;
  } catch {
    return { ...DEFAULT };
  }
  if (!raw) return { ...DEFAULT };

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { ...DEFAULT };
  }
  if (typeof parsed !== "object" || parsed === null) return { ...DEFAULT };

  // Each half is read on its own, so one bad field does not discard the other.
  const o = parsed as Record<string, unknown>;
  return {
    theme: isTheme(o.theme) ? o.theme : DEFAULT.theme,
    scale: clamp(o.scale),
  };
}

export function write(storage: Store | null, prefs: Prefs): void {
  try {
    storage?.setItem(KEY, JSON.stringify(prefs));
  } catch {
    // Storage full, or disabled. The preference still applies for this session;
    // failing to remember it is not worth interrupting anybody over.
  }
}

function isTheme(v: unknown): v is Theme {
  return v === "system" || v === "dark" || v === "light";
}

function clamp(v: unknown): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return DEFAULT.scale;
  const lo = SCALES[0];
  const hi = SCALES[SCALES.length - 1];
  return Math.min(hi, Math.max(lo, v));
}

/**
 * resolveTheme turns a preference into a look.
 *
 * "system" is not a third theme. It is a deferral, and it keeps deferring, so a
 * desktop that flips at sunset takes kubeside with it. Pinning dark or light
 * stops the tracking, which is the whole point of pinning.
 */
export function resolveTheme(theme: Theme, prefersDark: boolean): "dark" | "light" {
  if (theme === "system") return prefersDark ? "dark" : "light";
  return theme;
}

/** cycleTheme returns to following the system rather than stranding anyone on a pin. */
export function cycleTheme(theme: Theme): Theme {
  if (theme === "system") return "light";
  if (theme === "light") return "dark";
  return "system";
}

/**
 * step walks the ladder and stops at both ends, so a held key settles instead
 * of wrapping around to the opposite extreme.
 *
 * A value between rungs, from an older build or a hand edit, snaps to the
 * nearest one first. Otherwise it would drift off the ladder forever.
 */
export function step(scale: number, direction: 1 | -1): number {
  let nearest = 0;
  for (let i = 1; i < SCALES.length; i++) {
    if (Math.abs(SCALES[i] - scale) < Math.abs(SCALES[nearest] - scale)) nearest = i;
  }
  const next = nearest + direction;
  if (next < 0 || next >= SCALES.length) return SCALES[nearest];
  return SCALES[next];
}

/** The shape apply needs, which document.documentElement satisfies. */
export interface Root {
  setAttribute(name: string, value: string): void;
  style: { setProperty(name: string, value: string): void };
}

export function apply(root: Root, prefs: Prefs, prefersDark: boolean): void {
  root.setAttribute("data-theme", resolveTheme(prefs.theme, prefersDark));
  root.style.setProperty("--scale", String(prefs.scale));
}

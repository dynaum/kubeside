/**
 * The terminal's colours, taken from the theme in force.
 *
 * xterm.js paints to a canvas and cannot read CSS variables, so it has to be
 * handed literal colours. It used to be handed the dark palette as hex, which
 * meant a shell opened on a light screen was near-black text on a near-black
 * background while the rest of the product was paper.
 *
 * Pure, so the mapping is testable without a canvas: the caller supplies the
 * reader, which in the browser is getComputedStyle().getPropertyValue.
 */

export interface TermTheme {
  background: string;
  foreground: string;
  cursor: string;
  selectionBackground: string;
  red: string;
  green: string;
  yellow: string;
  blue: string;
  brightBlack: string;
}

/** Used only when a token resolves to nothing. An empty string makes xterm throw. */
const FALLBACK: Record<string, string> = {
  "--bg": "#0A0E10",
  "--fg": "#DEE7EA",
  "--fg-3": "#5A6C74",
  "--err": "#E5484D",
  "--warn": "#E0A72E",
  "--ok": "#4FB286",
  "--info": "#4C8DD9",
};

export function termThemeFrom(get: (token: string) => string): TermTheme {
  const v = (token: string) => get(token).trim() || FALLBACK[token];

  const fg = v("--fg");
  return {
    background: v("--bg"),
    foreground: fg,
    cursor: fg,
    // The status palette is what a shell's ANSI output lands on, so the colours
    // in a log line inside the terminal match the same meanings outside it.
    selectionBackground: v("--fg-3"),
    red: v("--err"),
    green: v("--ok"),
    yellow: v("--warn"),
    blue: v("--info"),
    brightBlack: v("--fg-3"),
  };
}

/** Reads the live tokens off an element, for the browser. */
export function termThemeOf(el: Element): TermTheme {
  const style = getComputedStyle(el);
  return termThemeFrom((token) => style.getPropertyValue(token));
}

/**
 * The terminal's font size, following the same scale as everything else.
 *
 * 12px is the data size the type foundation names, which is what the shell has
 * always used.
 */
export function termFontSize(el: Element): number {
  const raw = parseFloat(getComputedStyle(el).getPropertyValue("--scale"));
  const scale = Number.isFinite(raw) && raw > 0 ? raw : 1;
  return Math.round(12 * scale);
}

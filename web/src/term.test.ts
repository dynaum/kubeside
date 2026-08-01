import { describe, expect, it } from "vitest";
import { termThemeFrom } from "./term";

/** Stands in for getComputedStyle().getPropertyValue. */
function tokens(map: Record<string, string>) {
  return (name: string) => map[name] ?? "";
}

describe("termThemeFrom", () => {
  // xterm.js cannot read CSS variables. It was handed the dark palette as
  // literal hex, so a shell stayed dark on a light screen: black text on black.
  it("takes its colours from the tokens in force", () => {
    const light = termThemeFrom(tokens({
      "--bg": "#F4F2ED", "--fg": "#141B1F", "--fg-3": "#7C8A91",
      "--err": "#B02A26", "--warn": "#8A5D0B", "--ok": "#1E7A55", "--info": "#1F5E9E",
    }));
    expect(light.background).toBe("#F4F2ED");
    expect(light.foreground).toBe("#141B1F");
    expect(light.cursor).toBe("#141B1F");
  });

  it("maps the ANSI colours a shell actually emits", () => {
    const t = termThemeFrom(tokens({
      "--bg": "#0A0E10", "--fg": "#DEE7EA", "--fg-3": "#5A6C74",
      "--err": "#E5484D", "--warn": "#E0A72E", "--ok": "#4FB286", "--info": "#4C8DD9",
    }));
    expect(t.red).toBe("#E5484D");
    expect(t.green).toBe("#4FB286");
    expect(t.yellow).toBe("#E0A72E");
    expect(t.blue).toBe("#4C8DD9");
  });

  // Values arrive from getPropertyValue with leading whitespace.
  it("trims what the browser returns", () => {
    expect(termThemeFrom(tokens({ "--bg": "  #FFFFFF " })).background).toBe("#FFFFFF");
  });

  // A token that resolves to nothing must not become an empty string, which
  // xterm rejects at construction and takes the whole shell down with it.
  it("falls back rather than handing xterm an empty colour", () => {
    const t = termThemeFrom(tokens({}));
    expect(t.background).toMatch(/^#/);
    expect(t.foreground).toMatch(/^#/);
    expect(t.red).toMatch(/^#/);
  });
});

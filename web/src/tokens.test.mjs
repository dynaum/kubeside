import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = readFileSync(fileURLToPath(new URL("./tokens.css", import.meta.url)), "utf8");

/** The custom-property declarations inside the first block with this selector. */
function block(selector) {
  const at = css.indexOf(selector);
  expect(at, `no block for ${selector}`).toBeGreaterThan(-1);
  const open = css.indexOf("{", at);
  const close = css.indexOf("}", open);
  return css
    .slice(open + 1, close)
    .split(";")
    .map((d) => d.trim())
    .filter((d) => d.startsWith("--"));
}

describe("the light palette", () => {
  // The design system's own claim is that both themes come from one source, so
  // a contrast failure is a build error rather than a bug report. The CSP
  // forbids inline scripts, so the system default has to be answered in CSS,
  // which means the light palette is written twice. This is that build error.
  it("is identical wherever it is applied", () => {
    const chosen = block('[data-theme="light"]');
    const fromSystem = block(':root:not([data-theme="dark"])');
    expect(fromSystem).toEqual(chosen);
    expect(chosen.length).toBeGreaterThan(15);
  });

  it("defines every token the dark palette does", () => {
    const name = (d) => d.split(":")[0].trim();
    const dark = block(':root, [data-theme="dark"]').map(name).sort();
    const light = block('[data-theme="light"]').map(name).sort();
    expect(light).toEqual(dark);
  });
});

describe("the type scale", () => {
  const sizes = [...css.matchAll(/font-size:\s*([^;}]+)/g)].map((m) => m[1].trim());

  // A literal pixel here is a size nothing can turn. This is the whole reason
  // text was not adjustable: the seven sizes the design system names were
  // written out 44 times instead of being addressable once.
  it("has no size a preference cannot reach", () => {
    const literal = sizes.filter((s) => /^[0-9.]+px$/.test(s));
    expect(literal).toEqual([]);
  });

  it("derives every size from the multiplier", () => {
    expect(sizes.length).toBeGreaterThan(30);
    for (const s of sizes) {
      // inherit follows whatever it inherits from, which is the scale.
      if (s === "inherit") continue;
      expect(s, `${s} does not follow --scale`).toMatch(/var\(--fs-|var\(--scale\)/);
    }
  });

  // Row height is not decoration. The foundation calls 34px the tightest height
  // that still fits a 9px glyph and 12px mono, so text that grows inside a row
  // that does not is text that clips.
  it("scales the row with the text", () => {
    expect(css).toMatch(/--row-h:\s*calc\(34px\s*\*\s*var\(--scale\)\)/);
  });

  // These hold text and clip if it grows past them.
  it("scales the widths that hold text", () => {
    for (const px of ["216px", "132px", "96px", "112px"]) {
      const re = new RegExp(`calc\\(${px}\\s*\\*\\s*var\\(--scale\\)\\)`);
      expect(css, `${px} holds text and must scale`).toMatch(re);
    }
  });

  // The spacing grid deliberately does not move. That is the difference between
  // this and browser zoom, and the reason it costs fewer rows.
  it("leaves the spacing grid alone", () => {
    // Just the two lines that define the grid, not the rest of the block.
    const spacing = css.match(/--s1:.*\n.*--s5:[^\n]*/)[0];
    expect(spacing).toContain("4px");
    expect(spacing).not.toContain("--scale");
  });
});

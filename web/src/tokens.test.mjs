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

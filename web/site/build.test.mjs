import { describe, expect, it, beforeAll } from "vitest";
import { mkdtempSync, readFileSync, existsSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { buildSite, rewriteLink, slugOf, DOCS, REPO } from "./build.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..", "..");

// The docs site has one failure mode that matters: a page that quietly stops
// existing, or a link that points at nothing. Both read to a visitor as "this
// product has no answer for that", which is the paper version of rendering an
// unknown window as an empty one.

let out;
let pages;

beforeAll(() => {
  out = mkdtempSync(join(tmpdir(), "kubeside-site-"));
  buildSite({ root, out });
  pages = readdirSync(join(out, "docs")).filter((f) => f.endsWith(".html"));
});

describe("the docs site", () => {
  it("emits a page for every document in docs/", () => {
    const sources = readdirSync(join(root, "docs")).filter((f) => f.endsWith(".md"));
    expect(sources.length).toBeGreaterThan(0);
    for (const src of sources) {
      expect(pages).toContain(`${slugOf(src)}.html`);
    }
    expect(pages.length).toBe(sources.length);
  });

  it("emits a landing page", () => {
    expect(existsSync(join(out, "index.html"))).toBe(true);
  });

  // A dead internal link is the site telling a visitor a page exists when it
  // does not. Every one of them is checked, on every build.
  it("leaves no internal link pointing at nothing", () => {
    const dead = [];
    const files = ["index.html", ...pages.map((p) => `docs/${p}`)];
    for (const file of files) {
      const html = readFileSync(join(out, file), "utf8");
      for (const [, href] of html.matchAll(/href="([^"]+)"/g)) {
        if (/^(https?:|mailto:|#)/.test(href)) continue;
        const target = resolve(dirname(join(out, file)), href.split("#")[0]);
        if (!existsSync(target)) dead.push(`${file} -> ${href}`);
      }
    }
    expect(dead).toEqual([]);
  });

  // The docs cross-reference each other by filename. Those links have to end up
  // pointing at the rendered page, not at a raw markdown file nobody serves.
  it("rewrites a link between two documents to the page beside it", () => {
    expect(rewriteLink("04-multi-cluster.md")).toBe("04-multi-cluster.html");
    expect(rewriteLink("docs/01-problem.md")).toBe("docs/01-problem.html");
  });

  // CLAUDE.md is in the repository but not on the site. Dropping the link would
  // hide it; keeping it relative would break it. It goes to the repository.
  it("sends a link outside the site to the repository rather than nowhere", () => {
    const href = rewriteLink("../CLAUDE.md");
    expect(href.startsWith(REPO)).toBe(true);
    expect(href.endsWith("CLAUDE.md")).toBe(true);
  });

  it("leaves an external link alone", () => {
    const href = "https://github.com/derailed/k9s/issues/1017";
    expect(rewriteLink(href)).toBe(href);
  });

  // The docs carry their argument in tables and code blocks as much as in
  // prose. A renderer that flattened them would ship the words without the
  // evidence.
  it("renders tables and code blocks rather than flattening them", () => {
    const html = readFileSync(join(out, "docs", "05-architecture.html"), "utf8");
    expect(html).toContain("<table>");
    expect(html).toContain("<code>");
  });

  it("gives every heading an anchor so a section can be linked to", () => {
    const html = readFileSync(join(out, "docs", "03-product-spec.html"), "utf8");
    expect(/<h2 id="[^"]+">/.test(html)).toBe(true);
  });

  // The landing page shows the product, and the only honest screenshots are the
  // ones the visual gate already compares against.
  it("ships the screenshots the visual gate owns", () => {
    const index = readFileSync(join(out, "index.html"), "utf8");
    const shots = [...index.matchAll(/src="(shots\/[^"]+)"/g)].map((m) => m[1]);
    expect(shots.length).toBeGreaterThan(0);
    for (const shot of shots) {
      expect(existsSync(join(out, shot))).toBe(true);
    }
  });

  it("names every document in the navigation of every page", () => {
    for (const page of pages) {
      const html = readFileSync(join(out, "docs", page), "utf8");
      for (const doc of DOCS) {
        expect(html).toContain(`${slugOf(doc.file)}.html`);
      }
    }
  });

  // A site that claims the product is still a design document would be wrong on
  // its first line.
  it("does not describe the product as unbuilt", () => {
    const index = readFileSync(join(out, "index.html"), "utf8");
    expect(index.toLowerCase()).not.toContain("no code yet");
  });
});

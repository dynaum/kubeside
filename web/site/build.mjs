// The documentation site.
//
// It renders the documents in docs/ that the product was built from, beside a
// landing page that shows the product itself. Nothing here is a second source
// of truth: the pages are the repository's own markdown, and the screenshots
// are the images the visual gate compares every build against, so a site that
// drifts from the product fails a test rather than a reader.
//
// Zero runtime cost: this runs in CI, emits static files, and never ships in
// the binary.

import { cpSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { marked } from "marked";

export const REPO = "https://github.com/dynaum/kubeside";
export const SITE = "kubeside";

// Labels for the navigation. A document missing from this list still gets a
// page and a nav entry from its own first heading: the site never drops a
// document because somebody forgot to register it here.
export const DOCS = [
  { file: "01-problem.md", label: "The problem" },
  { file: "02-personas.md", label: "Personas" },
  { file: "03-product-spec.md", label: "Product spec" },
  { file: "04-multi-cluster.md", label: "Multi-cluster" },
  { file: "05-architecture.md", label: "Architecture" },
  { file: "06-roadmap.md", label: "Roadmap" },
];

// The screenshots are the visual gate's baselines. Using anything else would
// let the site show a product that no longer exists.
const SHOTS = [
  {
    file: "apps-chromium-linux.png",
    title: "Is my app up",
    caption:
      "Every app you own, in every cluster your kubeconfig reaches, grouped as apps rather than as ReplicaSets. A cluster that cannot be reached says so in its own row instead of contributing silence.",
  },
  {
    file: "app-detail-chromium-linux.png",
    title: "What changed, and when",
    caption:
      "The timeline is reconstructed from what the cluster still holds: ReplicaSets, ControllerRevisions, Helm release secrets, pod termination states. Where its knowledge ends, it draws the horizon rather than an empty axis.",
  },
  {
    file: "config-chromium-linux.png",
    title: "What the container actually received",
    caption:
      "Environment resolved through envFrom, ConfigMaps, Secrets, and the downward API, compared against the previous revision. Secret values stay masked because kubeside never fetched them.",
  },
  {
    file: "promotion-chromium-linux.png",
    title: "Is the fix in prod yet",
    caption:
      "One row per app, one column per environment, and a cell that says whether the version differs, is behind, or could not be read at all.",
  },
  {
    file: "palette-chromium-linux.png",
    title: "Everything from the keyboard",
    caption:
      "The first thing a k9s user tries. Every action the product has is reachable without the mouse.",
  },
];

export function slugOf(file) {
  return file.replace(/\.md$/, "");
}

// Links inside the documents point at markdown files, because they are also
// read on GitHub. On the site they must point at the rendered page, and a link
// out of docs/ must reach the repository rather than a 404.
export function rewriteLink(href) {
  if (!href || /^(https?:|mailto:|#)/.test(href)) return href;
  if (href.startsWith("../")) return `${REPO}/blob/main/${href.slice(3)}`;
  if (href.endsWith(".md")) return href.replace(/\.md$/, ".html");
  const [path, hash] = href.split("#");
  if (path.endsWith(".md")) return `${path.replace(/\.md$/, ".html")}${hash ? `#${hash}` : ""}`;
  return href;
}

function anchor(text) {
  return String(text)
    .toLowerCase()
    .replace(/[^\w\s-]/g, "")
    .trim()
    .replace(/\s+/g, "-");
}

function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function renderMarkdown(md) {
  const renderer = new marked.Renderer();
  renderer.heading = function ({ tokens, depth }) {
    const inner = this.parser.parseInline(tokens);
    const plain = tokens.map((t) => t.raw ?? "").join("");
    return `<h${depth} id="${anchor(plain)}">${inner}</h${depth}>\n`;
  };
  renderer.link = function ({ href, title, tokens }) {
    const text = this.parser.parseInline(tokens);
    const to = rewriteLink(href);
    const external = /^https?:/.test(to);
    const attrs = external ? ' target="_blank" rel="noopener"' : "";
    return `<a href="${to}"${title ? ` title="${escapeHtml(title)}"` : ""}${attrs}>${text}</a>`;
  };
  return marked.parse(md, { renderer });
}

function titleOf(md, fallback) {
  const m = md.match(/^#\s+(.+)$/m);
  return m ? m[1].trim() : fallback;
}

function shell({ title, description, body, depth, active }) {
  const up = depth === 0 ? "" : "../";
  const nav = navFor(active, depth);
  return `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escapeHtml(title)}</title>
<meta name="description" content="${escapeHtml(description)}">
<meta property="og:title" content="${escapeHtml(title)}">
<meta property="og:description" content="${escapeHtml(description)}">
<link rel="icon" type="image/svg+xml" href="${up}favicon.svg">
<link rel="stylesheet" href="${up}tokens.css">
<link rel="stylesheet" href="${up}site.css">
</head>
<body>
<header class="top">
  <a class="brand" href="${up}index.html">kubeside</a>
  <nav class="top-nav">
    <a href="${up}docs/03-product-spec.html">Docs</a>
    <a href="${REPO}" target="_blank" rel="noopener">GitHub</a>
    <a href="${REPO}/releases" target="_blank" rel="noopener">Download</a>
  </nav>
</header>
${body}
<footer class="foot">
  <span>Apache-2.0</span>
  <span>${nav.count} documents, rendered from the repository</span>
  <a href="${REPO}" target="_blank" rel="noopener">github.com/dynaum/kubeside</a>
</footer>
</body>
</html>
`;
}

let navEntries = [];

function navFor(active, depth) {
  const up = depth === 0 ? "docs/" : "";
  const items = navEntries
    .map(
      (e) =>
        `<a class="nav-item${e.slug === active ? " on" : ""}" href="${up}${e.slug}.html">${escapeHtml(
          e.label,
        )}</a>`,
    )
    .join("\n      ");
  return { html: items, count: navEntries.length };
}

function landing({ shots }) {
  const cards = shots
    .map(
      (s, i) => `  <figure class="shot${i === 0 ? " first" : ""}">
    <img src="shots/${s.file}" alt="${escapeHtml(s.title)}" width="1440" height="900">
    <figcaption><b>${escapeHtml(s.title)}</b> ${escapeHtml(s.caption)}</figcaption>
  </figure>`,
    )
    .join("\n");

  const docs = navEntries
    .map((e) => `    <a class="doc-card" href="docs/${e.slug}.html"><b>${escapeHtml(e.label)}</b></a>`)
    .join("\n");

  return `<main class="landing">
<section class="hero">
  <h1>A Kubernetes client scoped to the developer,<br>not the cluster operator.</h1>
  <p class="lede">One binary. It reads the kubeconfig already on your machine, connects every
  context, and answers four questions across qa, stg, and prod. It refuses to answer anything else.</p>
  <div class="qs">
    <span class="q"><i>1</i> Is my app up?</span>
    <span class="q"><i>2</i> What changed, and when?</span>
    <span class="q"><i>3</i> What do the logs say, across every pod at once?</span>
    <span class="q"><i>4</i> What configuration did the container actually receive?</span>
  </div>
  <div class="install">
    <pre class="cmd"><code>brew install dynaum/tap/kubeside</code></pre>
    <pre class="cmd"><code>kubeside</code></pre>
    <p class="note">macOS, Linux, Windows. No installer, no runtime, no setup step. If
    <code>kubectl</code> works, kubeside works.</p>
  </div>
</section>

<section class="shots">
${cards}
</section>

<section class="claims">
  <div class="claim">
    <h2>Nothing is written to disk</h2>
    <p>No database, no cache file, no history directory. The timeline is assembled from
    history Kubernetes already keeps and extended live while the process runs. Stop the
    server and nothing is left behind.</p>
  </div>
  <div class="claim">
    <h2>Absence of knowledge is not absence of a thing</h2>
    <p>An empty axis is never rendered as a quiet period. A horizon marks where kubeside's
    knowledge ends, a gap marks what it could not read, and a metric it could not take is
    reported as unavailable rather than as zero.</p>
  </div>
  <div class="claim">
    <h2>A control is never hidden for lack of permission</h2>
    <p>It is disabled, and it names the verb the cluster refused. The security boundary is
    the cluster's RBAC, checked with SelfSubjectAccessReview; the guardrails on top are
    ergonomics, and both answers travel together.</p>
  </div>
  <div class="claim">
    <h2>Credentials stay on your machine</h2>
    <p>The kubeconfig is read-only input. Exec credential plugins run natively, the browser
    never talks to an apiserver, and nothing is sent to a remote service.</p>
  </div>
</section>

<section class="refuses">
  <h2>What it will not do</h2>
  <p>No node view. No PersistentVolume browsing. No RBAC editor. No CRD browser. No Helm
  chart management. No cost reporting. No topology graph. No YAML editor beyond a read-only
  viewer. Each is a legitimate need belonging to somebody else's tool, and shipping any of
  them turns this into a general-purpose dashboard.</p>
</section>

<section class="docs">
  <h2>The documents it was built from</h2>
  <div class="doc-grid">
${docs}
  </div>
</section>
</main>`;
}

export function buildSite({ root, out }) {
  const docsDir = join(root, "docs");
  const files = readdirSync(docsDir)
    .filter((f) => f.endsWith(".md"))
    .sort();

  const labelled = new Map(DOCS.map((d) => [d.file, d.label]));
  const sources = files.map((file) => {
    const md = readFileSync(join(docsDir, file), "utf8");
    return { file, md, slug: slugOf(file), label: labelled.get(file) ?? titleOf(md, slugOf(file)) };
  });
  navEntries = sources.map(({ slug, label }) => ({ slug, label }));

  rmSync(out, { recursive: true, force: true });
  mkdirSync(join(out, "docs"), { recursive: true });
  mkdirSync(join(out, "shots"), { recursive: true });

  const here = dirname(fileURLToPath(import.meta.url));
  cpSync(join(here, "..", "src", "tokens.css"), join(out, "tokens.css"));
  cpSync(join(here, "site.css"), join(out, "site.css"));
  cpSync(join(here, "favicon.svg"), join(out, "favicon.svg"));

  const shotsDir = join(here, "..", "e2e", "screens.spec.ts-snapshots");
  const shots = SHOTS.filter((s) => {
    try {
      cpSync(join(shotsDir, s.file), join(out, "shots", s.file));
      return true;
    } catch {
      // A screenshot the visual gate no longer produces is dropped rather than
      // rendered as a broken image. The gate is what keeps this list honest.
      return false;
    }
  });

  writeFileSync(
    join(out, "index.html"),
    shell({
      title: "kubeside — a Kubernetes client scoped to the developer",
      description:
        "One binary that reads your kubeconfig and answers four questions across qa, stg, and prod: is my app up, what changed, what do the logs say, what configuration did the container receive.",
      body: landing({ shots }),
      depth: 0,
      active: "",
    }),
  );

  for (const { md, slug, label } of sources) {
    const nav = navFor(slug, 1);
    const body = `<main class="doc">
  <nav class="side">
      ${nav.html}
  </nav>
  <article class="prose">
${renderMarkdown(md)}
  </article>
</main>`;
    writeFileSync(
      join(out, "docs", `${slug}.html`),
      shell({
        title: `${label} — ${SITE}`,
        description: `${label}: one of the documents kubeside was built from.`,
        body,
        depth: 1,
        active: slug,
      }),
    );
  }

  return { pages: sources.length, shots: shots.length, out };
}

// Running the file builds the site; importing it does not.
if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const here = dirname(fileURLToPath(import.meta.url));
  const root = resolve(here, "..", "..");
  const out = resolve(here, "..", "site-dist");
  const result = buildSite({ root, out });
  console.log(`site: ${result.pages} documents, ${result.shots} screenshots -> ${result.out}`);
}

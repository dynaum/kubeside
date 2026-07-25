// The documentation site.
//
// It renders the guide and the design documents from the repository's own
// markdown, beside a landing page that shows the product itself. Nothing here
// is a second source of truth: the screenshots and the walkthrough come from
// the fixture build the visual gate compares against, so a site that drifts
// from the product fails a test rather than a reader.
//
// Zero runtime cost: this runs in CI, emits static files, and never ships in
// the binary.

import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { marked } from "marked";

export const REPO = "https://github.com/dynaum/kubeside";
export const SITE = "kubeside";

// Two sections, in the order a visitor needs them: how to use it, then why it
// is shaped this way. A document missing from `pages` below still gets a page
// and an index entry, titled from its own first heading: the site never drops a
// document because somebody forgot to register it.
export const SECTIONS = [
  {
    id: "guide",
    label: "Guide",
    dir: "docs/guide",
    blurb: "Install it, run it, and use the four screens.",
    pages: {
      "01-install.md": ["Install and run", "One binary, no runtime, no setup step."],
      "02-first-run.md": ["First run", "What happens when it reads your kubeconfig, and how environments are inferred."],
      "03-apps.md": ["The app list", "How objects become apps, and how health is derived."],
      "04-logs.md": ["Logs", "Every replica merged into one stream, with its availability edges marked."],
      "05-timeline.md": ["The timeline", "History reconstructed from the cluster, and where its knowledge ends."],
      "06-configuration.md": ["Resolved configuration", "What the container actually received, and what changed since the last revision."],
      "07-promotion.md": ["Promotion and drift", "Is the fix in prod yet, and how do two environments differ."],
      "08-actions.md": ["Port-forward, exec, guardrails", "The three things that write, and the two gates in front of them."],
      "09-keyboard.md": ["Keyboard", "The command palette, and everything reachable from it."],
      "10-config-file.md": ["The config file", "Optional, strict, and never written by kubeside."],
      "11-permissions.md": ["Permissions", "What it reads, what it asks the cluster, and why nothing is hidden."],
      "12-troubleshooting.md": ["When things are missing", "Unreachable clusters, expired sessions, refused reads, absent metrics."],
    },
  },
  {
    id: "design",
    label: "Design notes",
    dir: "docs",
    blurb: "The research and the decisions the product was built from.",
    pages: {
      "01-problem.md": ["The problem", "Landscape research, complaint clusters, the four uncovered gaps."],
      "02-personas.md": ["Personas", "Five users, one stakeholder, three anti-personas."],
      "03-product-spec.md": ["Product spec", "Principles, the four screens, non-goals, anti-requirements."],
      "04-multi-cluster.md": ["Multi-cluster", "Environments, promotion, cross-environment diff, prod guardrails."],
      "05-architecture.md": ["Architecture", "Stack, data flow, storage, auth, performance budget."],
      "06-roadmap.md": ["Roadmap", "Milestones and what shipped in each."],
    },
  },
];

// The screenshots on the landing page. Every one is taken from the same fixture
// build the visual gate owns, so the site cannot show a product that no longer
// exists.
const SHOTS = [
  {
    file: "apps.png",
    title: "Is my app up",
    caption:
      "Every app you own, in every cluster your kubeconfig reaches, grouped as apps rather than as ReplicaSets. The GROUPED BY column says which rule produced each row, and a cluster that cannot be reached says so in its own row instead of contributing silence.",
  },
  {
    file: "app-detail.png",
    title: "What changed, and when",
    caption:
      "Reconstructed from what the cluster still holds: ReplicaSets, ControllerRevisions, Helm release secrets, pod termination states. Where its knowledge ends it draws the horizon, and a source your role cannot read becomes a labelled gap rather than a quiet period.",
  },
  {
    file: "config.png",
    title: "What the container actually received",
    caption:
      "Environment resolved through envFrom, ConfigMaps, Secrets, and the downward API, each key attributed to its source and compared against the previous revision. Secret values stay masked because kubeside never fetched them.",
  },
  {
    file: "promotion.png",
    title: "Is the fix in prod yet",
    caption:
      "One row per app, one column per environment. A version prod has that staging does not floats to the top, because a hotfix nobody back-merged is the worst thing on this screen.",
  },
];

export function slugOf(file) {
  return file.replace(/\.md$/, "");
}

// Links inside the documents point at markdown files, because they are also
// read on GitHub. On the site they must point at the rendered page, and a link
// out of the documentation must reach the repository rather than a 404.
export function rewriteLink(href) {
  if (!href || /^(https?:|mailto:|#)/.test(href)) return href;
  // ../images/x.png climbs out of docs/guide on GitHub and out of docs/ on the
  // site, which lands in the same place either way.
  if (href.startsWith("../images/")) return href;
  if (href.startsWith("../")) return `${REPO}/blob/main/${href.slice(3)}`;
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
  renderer.image = function ({ href, text }) {
    // A screenshot in the guide is the point of the paragraph around it, so it
    // gets the same frame the landing page uses.
    return `<img class="figure" src="${rewriteLink(href)}" alt="${escapeHtml(text ?? "")}" loading="lazy">`;
  };
  return marked.parse(md, { renderer });
}

function titleOf(md, fallback) {
  const m = md.match(/^#\s+(.+)$/m);
  return m ? m[1].trim() : fallback;
}

// Sections, resolved against the repository. Held at module scope because the
// navigation is identical on every page.
let nav = [];

function readSections(root) {
  return SECTIONS.map((section) => {
    const dir = join(root, section.dir);
    const files = readdirSync(dir)
      .filter((f) => f.endsWith(".md"))
      .sort();
    const docs = files.map((file) => {
      const md = readFileSync(join(dir, file), "utf8");
      const [label, blurb] = section.pages[file] ?? [titleOf(md, slugOf(file)), ""];
      return { file, md, slug: slugOf(file), label, blurb };
    });
    return { ...section, docs };
  });
}

function navHtml(active, { top = false } = {}) {
  return nav
    .map((section) => {
      const items = section.docs
        .map(
          (d) =>
            `<a class="nav-item${d.slug === active ? " on" : ""}" href="${top ? "docs/" : ""}${d.slug}.html">${escapeHtml(
              d.label,
            )}</a>`,
        )
        .join("\n        ");
      return `<div class="nav-group">
        <span class="nav-label">${escapeHtml(section.label)}</span>
        ${items}
      </div>`;
    })
    .join("\n      ");
}

function shell({ title, description, body, depth, active, wide = false }) {
  const up = depth === 0 ? "" : "../";
  return `<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escapeHtml(title)}</title>
<meta name="description" content="${escapeHtml(description)}">
<meta property="og:title" content="${escapeHtml(title)}">
<meta property="og:description" content="${escapeHtml(description)}">
<meta property="og:image" content="${up}images/apps.png">
<meta name="twitter:card" content="summary_large_image">
<link rel="icon" type="image/svg+xml" href="${up}favicon.svg">
<link rel="stylesheet" href="${up}tokens.css">
<link rel="stylesheet" href="${up}site.css">
</head>
<body${wide ? ' class="wide"' : ""} data-page="${escapeHtml(active || "landing")}">
<header class="top">
  <a class="brand" href="${up}index.html">kubeside</a>
  <nav class="top-nav">
    <a href="${up}docs/index.html">Documentation</a>
    <a href="${up}docs/01-install.html">Install</a>
    <a href="${REPO}" target="_blank" rel="noopener">GitHub</a>
  </nav>
</header>
${body}
<footer class="foot">
  <span>Apache-2.0</span>
  <span>Generated from the repository. Nothing here is written twice.</span>
  <a href="${REPO}" target="_blank" rel="noopener">github.com/dynaum/kubeside</a>
</footer>
</body>
</html>
`;
}

function landing({ shots, hasVideo }) {
  const video = hasVideo
    ? `<section class="reel">
  <video src="images/walkthrough.webm" poster="images/apps.png" autoplay muted loop playsinline></video>
  <p class="reel-cap">The app list, the timeline, the resolved configuration, the promotion matrix,
  and the palette that reaches all of them.</p>
</section>`
    : "";

  const cards = shots
    .map(
      (s) => `  <figure class="shot">
    <img src="images/${s.file}" alt="${escapeHtml(s.title)}" loading="lazy">
    <figcaption><b>${escapeHtml(s.title)}</b> ${escapeHtml(s.caption)}</figcaption>
  </figure>`,
    )
    .join("\n");

  const sections = nav
    .map(
      (section) => `    <div class="doc-col">
      <h3>${escapeHtml(section.label)}</h3>
      <p>${escapeHtml(section.blurb)}</p>
      ${section.docs
        .map((d) => `<a class="doc-link" href="docs/${d.slug}.html">${escapeHtml(d.label)}</a>`)
        .join("\n      ")}
    </div>`,
    )
    .join("\n");

  return `<main class="landing">
<section class="hero">
  <h1>Your apps, across every cluster,<br>without thinking in ReplicaSets.</h1>
  <p class="lede">A Kubernetes client scoped to the developer who ships the app, not the operator
  who runs the cluster. One binary reads the kubeconfig you already have and answers four
  questions across qa, stg, and prod. It refuses to answer anything else.</p>
  <div class="qs">
    <span class="q"><i>1</i> Is my app up?</span>
    <span class="q"><i>2</i> What changed, and when?</span>
    <span class="q"><i>3</i> What do the logs say, across every pod at once?</span>
    <span class="q"><i>4</i> What configuration did the container actually receive?</span>
  </div>
  <div class="install">
    <pre class="cmd"><code>brew install dynaum/tap/kubeside</code></pre>
    <pre class="cmd"><code>kubeside</code></pre>
    <p class="note">macOS, Linux, Windows. No installer, no runtime, no setup step, no agent in
    your cluster. If <code>kubectl</code> works, kubeside works.
    <a href="docs/01-install.html">Install guide →</a></p>
  </div>
</section>

${video}

<section class="problem">
  <h2>The problem</h2>
  <div class="cols">
    <p>Every Kubernetes UI mirrors the API tree: pick a resource kind, browse instances, pick a
    cluster from a switcher. That is the right shape for the person who runs the cluster. It is
    the wrong shape for the person who ships an app.</p>
    <p>A developer thinks in services, not ReplicaSets. Their unit of work is one app across qa,
    stg, and prod, and their questions are historical more often than live: what changed, who
    changed it, what did the container actually get. Answering any of those today means three
    terminals, a tab per replica, and a guess.</p>
    <p>So the dashboards end up as launchers for <code>kubectl</code>, and the four questions
    stay unanswered. Not because they are hard, but because nobody built the screen for
    them.</p>
  </div>
</section>

<section class="shots">
${cards}
</section>

<section class="claims">
  <div class="claim">
    <h2>Nothing is written to disk</h2>
    <p>No database, no cache file, no history directory. The timeline is assembled from history
    Kubernetes already keeps and extended live while the process runs. Stop the server and
    nothing is left behind.</p>
  </div>
  <div class="claim">
    <h2>Absence of knowledge is not absence of a thing</h2>
    <p>An empty axis is never rendered as a quiet period. A horizon marks where kubeside's
    knowledge ends, a gap marks what it could not read, and a metric it could not take is
    reported as unavailable rather than as zero.</p>
  </div>
  <div class="claim">
    <h2>A control is never hidden for lack of permission</h2>
    <p>It is disabled, and it names the verb the cluster refused. The security boundary is the
    cluster's RBAC, checked with SelfSubjectAccessReview; the guardrails on top are ergonomics,
    and both answers travel together.</p>
  </div>
  <div class="claim">
    <h2>Credentials stay on your machine</h2>
    <p>The kubeconfig is read-only input. Exec credential plugins run natively, the browser never
    talks to an apiserver, and nothing is sent to a remote service.</p>
  </div>
</section>

<section class="refuses">
  <h2>What it will not do</h2>
  <p>No node view. No PersistentVolume browsing. No RBAC editor. No CRD browser. No Helm chart
  management. No cost reporting. No topology graph. No YAML editor beyond a read-only viewer.
  Each is a legitimate need belonging to somebody else's tool, and shipping any of them turns
  this into a general-purpose dashboard.</p>
</section>

<section class="docs">
  <h2>Documentation</h2>
  <div class="doc-cols">
${sections}
  </div>
</section>
</main>`;
}

function docsIndex() {
  const sections = nav
    .map(
      (section) => `  <section class="index-section">
    <h2>${escapeHtml(section.label)}</h2>
    <p class="index-blurb">${escapeHtml(section.blurb)}</p>
    <div class="index-grid">
      ${section.docs
        .map(
          (d) => `<a class="index-card" href="${d.slug}.html">
        <b>${escapeHtml(d.label)}</b>
        <span>${escapeHtml(d.blurb)}</span>
      </a>`,
        )
        .join("\n      ")}
    </div>
  </section>`,
    )
    .join("\n");

  return `<main class="index-page">
  <h1>Documentation</h1>
  <p class="lede">The guide first, then the design notes the product was built from. Every page
  is generated from markdown in the repository, so none of it can drift from what is
  committed.</p>
${sections}
</main>`;
}

export function buildSite({ root, out }) {
  nav = readSections(root);

  const seen = new Set();
  for (const section of nav) {
    for (const d of section.docs) {
      if (seen.has(d.slug)) throw new Error(`two documents would render to ${d.slug}.html`);
      seen.add(d.slug);
    }
  }

  rmSync(out, { recursive: true, force: true });
  mkdirSync(join(out, "docs"), { recursive: true });
  mkdirSync(join(out, "images"), { recursive: true });

  const here = dirname(fileURLToPath(import.meta.url));
  cpSync(join(here, "..", "src", "tokens.css"), join(out, "tokens.css"));
  cpSync(join(here, "site.css"), join(out, "site.css"));
  cpSync(join(here, "favicon.svg"), join(out, "favicon.svg"));

  // Screenshots and the walkthrough. The GIF stays out: it exists for the
  // README, where a video element is not an option, and it is twice the size of
  // the webm for worse pixels.
  const imagesDir = join(root, "docs", "images");
  const media = existsSync(imagesDir)
    ? readdirSync(imagesDir).filter((f) => f.endsWith(".png") || f.endsWith(".webm"))
    : [];
  for (const file of media) {
    cpSync(join(imagesDir, file), join(out, "images", file));
  }
  const shots = SHOTS.filter((s) => media.includes(s.file));
  const hasVideo = media.includes("walkthrough.webm");

  writeFileSync(
    join(out, "index.html"),
    shell({
      title: "kubeside — a Kubernetes client scoped to the developer",
      description:
        "One binary that reads your kubeconfig and answers four questions across qa, stg, and prod: is my app up, what changed, what do the logs say, what configuration did the container receive.",
      body: landing({ shots, hasVideo }),
      depth: 0,
      active: "",
      wide: true,
    }),
  );

  writeFileSync(
    join(out, "docs", "index.html"),
    shell({
      title: `Documentation — ${SITE}`,
      description: "The guide and the design notes kubeside was built from.",
      body: docsIndex(),
      depth: 1,
      active: "index",
      wide: true,
    }),
  );

  for (const section of nav) {
    for (const { md, slug, label, blurb } of section.docs) {
      const body = `<main class="doc">
  <nav class="side">
      ${navHtml(slug)}
  </nav>
  <article class="prose">
${renderMarkdown(md)}
  </article>
</main>`;
      writeFileSync(
        join(out, "docs", `${slug}.html`),
        shell({
          title: `${label} — ${SITE}`,
          description: blurb || `${label}, from the kubeside documentation.`,
          body,
          depth: 1,
          active: slug,
        }),
      );
    }
  }

  const pages = nav.reduce((n, s) => n + s.docs.length, 0);
  return { pages, shots: shots.length, media: media.length, out };
}

// Running the file builds the site; importing it does not.
if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const here = dirname(fileURLToPath(import.meta.url));
  const root = resolve(here, "..", "..");
  const out = resolve(here, "..", "site-dist");
  const result = buildSite({ root, out });
  console.log(`site: ${result.pages} documents, ${result.media} media files -> ${result.out}`);
}

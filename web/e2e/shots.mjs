// Marketing screenshots and the walkthrough recording.
//
// Same fixture server the visual gate uses, so what a visitor sees on the
// README and the site is the product as the gate renders it, not a mockup.
// This is deliberately not a spec file: the gate compares pixels and must not
// grow a test whose job is to overwrite images.
//
//   node e2e/shots.mjs           # screenshots into docs/images
//   node e2e/shots.mjs --video   # also record the walkthrough (needs ffmpeg)

import { chromium } from "@playwright/test";
import { spawn } from "node:child_process";
import { mkdirSync, rmSync, readdirSync, renameSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..");
const outDir = join(root, "docs", "images");
const port = Number(process.env.PORT ?? 4399);
const base = `http://127.0.0.1:${port}`;
const token = "?t=fixture-token";
const FROZEN = new Date("2026-07-25T03:00:00Z");

const SCREENS = [
  { name: "apps", hash: "#apps/qa1", ready: "table.tbl tbody tr" },
  { name: "app-detail", hash: "#app/qa1/team-a/payments", ready: ".stat-value" },
  { name: "config", hash: "#config/qa1/team-a/payments", ready: ".kv, table.tbl" },
  { name: "promotion", hash: "#promotion", ready: "table.matrix tbody tr" },
];

function startServer() {
  const proc = spawn("node", [join(here, "server.mjs")], {
    env: { ...process.env, PORT: String(port), FIXTURE_WS: "1" },
    stdio: "ignore",
  });
  return proc;
}

async function waitForServer() {
  for (let i = 0; i < 100; i++) {
    try {
      const res = await fetch(`${base}/index.html`);
      if (res.ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("fixture server did not start");
}

// The screens are dense at the top and the window is 900px tall, so a raw
// capture is mostly background. This measures where the content actually ends.
async function contentHeight(page) {
  return page.evaluate(() => {
    // Leaves only: a full-height shell or rail would answer "the whole window"
    // and defeat the measurement.
    let bottom = 0;
    for (const el of document.querySelectorAll("body *")) {
      if (el.childElementCount > 0) continue;
      if (!(el.textContent ?? "").trim() && !el.matches("img, svg, canvas, input")) continue;
      const r = el.getBoundingClientRect();
      if (r.height > 0 && r.width > 0 && r.bottom > bottom && r.bottom <= window.innerHeight) {
        bottom = r.bottom;
      }
    }
    return Math.round(bottom);
  });
}

async function main() {
  const wantVideo = process.argv.includes("--video");
  const server = startServer();
  process.on("exit", () => server.kill());
  await waitForServer();

  mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch();

  const shoot = async (page, name) => {
    const height = Math.min(900, Math.max(460, (await contentHeight(page)) + 24));
    await page.screenshot({
      path: join(outDir, `${name}.png`),
      clip: { x: 0, y: 0, width: 1440, height },
    });
    console.log(`shot: docs/images/${name}.png (${1440}x${height})`);
  };

  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: "dark",
    deviceScaleFactor: 2,
  });
  const page = await context.newPage();
  await page.clock.setFixedTime(FROZEN);

  for (const screen of SCREENS) {
    await page.goto(`${base}/${token}${screen.hash}`);
    await page.waitForSelector(screen.ready);
    await page.waitForTimeout(300);
    await shoot(page, screen.name);
  }

  // The palette is a state, not a route.
  await page.goto(`${base}/${token}#apps/qa1`);
  await page.waitForSelector("table.tbl tbody tr");
  await page.keyboard.press("ControlOrMeta+k");
  await page.waitForSelector(".palette");
  await page.waitForTimeout(200);
  await page.screenshot({ path: join(outDir, "palette.png"), clip: { x: 0, y: 0, width: 1440, height: 620 } });
  console.log("shot: docs/images/palette.png (1440x620)");
  await context.close();

  if (wantVideo) {
    await record(browser);
  }

  await browser.close();
  server.kill();
}

// A thirty-second walkthrough: the app list, the timeline, the configuration,
// the promotion matrix, and the palette that reaches all of them.
async function record(browser) {
  const videoDir = join(here, "..", "video-tmp");
  rmSync(videoDir, { recursive: true, force: true });

  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    colorScheme: "dark",
    recordVideo: { dir: videoDir, size: { width: 1280, height: 800 } },
  });
  const page = await context.newPage();
  await page.clock.setFixedTime(FROZEN);

  const beat = (ms = 1400) => page.waitForTimeout(ms);

  await page.goto(`${base}/${token}#apps/qa1`);
  await page.waitForSelector("table.tbl tbody tr");
  await beat(2200);

  await page.goto(`${base}/${token}#app/qa1/team-a/payments`);
  await page.waitForSelector(".stat-value");
  await beat(2600);

  await page.goto(`${base}/${token}#config/qa1/team-a/payments`);
  await page.waitForSelector("table.tbl, .kv");
  await beat(2600);

  await page.goto(`${base}/${token}#promotion`);
  await page.waitForSelector("table.matrix tbody tr");
  await beat(2600);

  await page.goto(`${base}/${token}#apps/qa1`);
  await page.waitForSelector("table.tbl tbody tr");
  await beat(600);
  await page.keyboard.press("ControlOrMeta+k");
  await page.waitForSelector(".palette");
  await beat(700);
  for (const ch of "pay") {
    await page.keyboard.type(ch);
    await beat(220);
  }
  await beat(1200);
  await page.keyboard.press("Enter");
  await page.waitForSelector(".stat-value");
  await beat(2000);

  await context.close();

  const file = readdirSync(videoDir).find((f) => f.endsWith(".webm"));
  if (!file) throw new Error("no video recorded");
  const webm = join(outDir, "walkthrough.webm");
  renameSync(join(videoDir, file), webm);
  rmSync(videoDir, { recursive: true, force: true });
  console.log(`video: docs/images/walkthrough.webm`);
}

main().catch((err) => {
  console.error(err);
  // The fixture server is a child process; leaving it holding the port would
  // make the next run silently reuse a stale one.
  process.exit(1);
});

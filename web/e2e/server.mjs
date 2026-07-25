// A stand-in for the Go server: the built UI plus canned API answers.
//
// The screenshots exist to catch design regressions, so the data behind them
// has to be a constant. Pointing them at a real cluster would make every run a
// coin flip and teach the team to ignore the gate.

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";
import * as fx from "./fixtures.mjs";

const dist = join(fileURLToPath(new URL(".", import.meta.url)), "..", "dist");
const port = Number(process.env.PORT ?? 4321);

const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
};

function json(res, body) {
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body));
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");

  if (url.pathname.startsWith("/api/")) {
    switch (url.pathname) {
      case "/api/contexts": return json(res, fx.contexts);
      case "/api/apps": return json(res, { ...fx.appsView, context: url.searchParams.get("context") ?? "qa1" });
      case "/api/app": return json(res, fx.appDetail);
      case "/api/config": return json(res, fx.configView);
      case "/api/promotion": return json(res, fx.promotionView);
      case "/api/can": return json(res, fx.capabilities);
      case "/api/forwards": return json(res, fx.forwards);
      case "/api/timeline": return json(res, fx.appDetail.timeline);
    }
    res.writeHead(404, { "Content-Type": "application/json" });
    return res.end(`{"error":"no fixture for ${url.pathname}"}`);
  }

  // Everything else is the built bundle, with index.html as the fallback so a
  // hash route loads directly.
  const path = url.pathname === "/" ? "/index.html" : url.pathname;
  try {
    const body = await readFile(join(dist, path));
    res.writeHead(200, { "Content-Type": types[extname(path)] ?? "application/octet-stream" });
    res.end(body);
  } catch {
    const body = await readFile(join(dist, "index.html"));
    res.writeHead(200, { "Content-Type": types[".html"] });
    res.end(body);
  }
});

server.listen(port, "127.0.0.1", () => console.log(`fixtures on http://127.0.0.1:${port}`));

// A stand-in for the Go server: the built UI plus canned API answers.
//
// The screenshots exist to catch design regressions, so the data behind them
// has to be a constant. Pointing them at a real cluster would make every run a
// coin flip and teach the team to ignore the gate.

import { createServer } from "node:http";
import { createHash } from "node:crypto";
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
      case "/api/fleet": return json(res, fx.fleetView);
      case "/api/can": return json(res, fx.capabilities);
      case "/api/forwards": return json(res, fx.forwards);
      case "/api/timeline": return json(res, fx.appDetail.timeline);
      case "/api/gate": return json(res, fx.gateView);
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

// The delta stream, and only when asked for.
//
// The visual baselines were taken against a server that refuses the upgrade, so
// they show the header in its reconnecting state; turning that on by default
// would fail the gate everywhere for a cosmetic reason. The marketing shots ask
// for it explicitly, because a screenshot of a product that cannot connect is
// not a screenshot of the product.
//
// Just enough of RFC 6455 to answer a subscription with one snapshot: the
// frames here are small, text, and never fragmented, which is all the client
// sends.
function decodeFrames(buf) {
  const out = [];
  let i = 0;
  while (i + 2 <= buf.length) {
    const opcode = buf[i] & 0x0f;
    const masked = (buf[i + 1] & 0x80) !== 0;
    let len = buf[i + 1] & 0x7f;
    let off = i + 2;
    if (len === 126) {
      len = buf.readUInt16BE(off);
      off += 2;
    } else if (len === 127) {
      len = Number(buf.readBigUInt64BE(off));
      off += 8;
    }
    let mask;
    if (masked) {
      mask = buf.subarray(off, off + 4);
      off += 4;
    }
    if (off + len > buf.length) break;
    const payload = Buffer.from(buf.subarray(off, off + len));
    if (mask) for (let k = 0; k < payload.length; k++) payload[k] ^= mask[k % 4];
    if (opcode === 0x1) out.push(payload.toString("utf8"));
    i = off + len;
  }
  return out;
}

function encodeFrame(text) {
  const payload = Buffer.from(text, "utf8");
  const head = payload.length < 126
    ? Buffer.from([0x81, payload.length])
    : Buffer.concat([Buffer.from([0x81, 126]), (() => {
        const b = Buffer.alloc(2);
        b.writeUInt16BE(payload.length);
        return b;
      })()]);
  return Buffer.concat([head, payload]);
}

if (process.env.FIXTURE_WS === "1") {
  server.on("upgrade", (req, socket) => {
    const key = req.headers["sec-websocket-key"];
    if (!key) return socket.destroy();
    const accept = createHash("sha1")
      .update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
      .digest("base64");
    socket.write(
      "HTTP/1.1 101 Switching Protocols\r\n" +
        "Upgrade: websocket\r\n" +
        "Connection: Upgrade\r\n" +
        `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
    );

    let seq = 0;
    socket.on("data", (chunk) => {
      for (const text of decodeFrames(chunk)) {
        let msg;
        try {
          msg = JSON.parse(text);
        } catch {
          continue;
        }
        if (msg.type !== "subscribe" || msg.view !== "apps") continue;
        const context = msg.context ?? "qa1";
        socket.write(encodeFrame(JSON.stringify({
          type: "snapshot",
          view: "apps",
          context,
          key: `apps|${context}`,
          seq: ++seq,
          snapshot: { ...fx.appsView, context },
        })));
      }
    });
    socket.on("error", () => socket.destroy());
  });
}

server.listen(port, "127.0.0.1", () => console.log(`fixtures on http://127.0.0.1:${port}`));

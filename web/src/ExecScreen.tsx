import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import type { ContextView, PodView } from "./api";
import { api } from "./api";
import { Confirm } from "./Confirm";
import { termFontSize, termThemeOf } from "./term";
import { envToken } from "./health";

// A shell in a container, proxied through the local server.
//
// The browser never talks to an apiserver: keystrokes go to 127.0.0.1 and the
// credentials stay in the Go process, exactly as every read does. Two gates
// stand in front of this screen, and both refuse in words rather than by
// leaving a blank terminal.

export function ExecScreen({
  context, namespace, workload, onBack,
}: {
  context: ContextView;
  namespace: string;
  workload: string;
  onBack: () => void;
}) {
  const mount = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);

  // The terminal repaints when the theme or the text size changes, without the
  // session noticing. Watching the root element rather than taking the
  // preference as a prop covers the case nothing else would: a desktop that
  // flips to light at sunset while somebody is holding a shell open.
  useEffect(() => {
    const root = document.documentElement;
    const repaint = () => {
      const term = termRef.current;
      if (!term) return;
      term.options.theme = termThemeOf(root);
      term.options.fontSize = termFontSize(root);
      // The character cell just changed size, so the pty needs the new
      // dimensions or the shell keeps wrapping at the old width.
      fitRef.current?.fit();
    };
    const observer = new MutationObserver(repaint);
    observer.observe(root, { attributeFilter: ["data-theme", "style"] });
    return () => observer.disconnect();
  }, []);
  const [pods, setPods] = useState<PodView[]>([]);
  const [pod, setPod] = useState("");
  const [note, setNote] = useState<string | null>(null);
  // What the developer typed, when the environment asks for it. The socket is
  // not opened until it exists, because the server compares it and would refuse
  // in a blank terminal, which is the worst place to learn about a refusal.
  const [confirm, setConfirm] = useState(context.write === "allow" ? "" : null);

  useEffect(() => {
    let alive = true;
    api.app(context.name, namespace, workload)
      .then((v) => {
        if (!alive) return;
        setPods(v.pods);
        // A ready replica by default: a shell in a crash-looping pod closes as
        // fast as it opens.
        const ready = v.pods.find((p) => p.health === "healthy") ?? v.pods[0];
        if (ready) setPod(ready.name);
      })
      .catch((e) => setNote(String(e.message ?? e)));
    return () => { alive = false; };
  }, [context.name, namespace, workload]);

  useEffect(() => {
    if (!pod || confirm === null || !mount.current) return;

    const root = document.documentElement;
    const term = new Terminal({
      fontFamily: "var(--font-mono), monospace",
      fontSize: termFontSize(root),
      theme: termThemeOf(root),
      cursorBlink: true,
    });
    termRef.current = term;
    const fit = new FitAddon();
    fitRef.current = fit;
    term.loadAddon(fit);
    term.open(mount.current);
    fit.fit();

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const query = new URLSearchParams({
      context: context.name, namespace, pod, container: "", workload, confirm,
    });
    const ws = new WebSocket(`${proto}//${window.location.host}/api/exec?${query}`);
    ws.binaryType = "arraybuffer";

    const send = () => {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    };

    ws.onopen = () => {
      send();
      term.focus();
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        // Control messages: a refusal or an ending, said in words rather than
        // by dropping the connection.
        try {
          const m = JSON.parse(ev.data) as { type: string; message?: string };
          term.writeln(`\r\n\x1b[33m${m.message ?? m.type}\x1b[0m`);
          if (m.type === "error") setNote(m.message ?? "refused");
        } catch {
          term.write(ev.data);
        }
        return;
      }
      term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onclose = () => term.writeln("\r\n\x1b[90mconnection closed\x1b[0m");

    const typed = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(data));
    });

    const onResize = () => { fit.fit(); send(); };
    window.addEventListener("resize", onResize);

    return () => {
      window.removeEventListener("resize", onResize);
      typed.dispose();
      ws.close();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, [context.name, namespace, workload, pod, confirm]);

  return (
    <>
      {/* Waits for the pod. The dialog's equivalent command names it, and a
          dialog that opened a moment earlier would have had nothing to name. */}
      {confirm === null && pod !== "" && (
        <Confirm
          context={context.name}
          namespace={namespace}
          verb="exec"
          resource="pod"
          name={workload}
          pod={pod}
          onCancel={onBack}
          onConfirm={setConfirm}
        />
      )}
      <div className={`topbar env-edge${context.hazard ? " hazard" : ""}`} data-env={envToken(context)}>
        <span className="env-chip">{context.environment}</span>
        <div className="crumb">
          <button className="tab" style={{ padding: 0 }} onClick={onBack}>apps</button>
          <span className="sep">/</span>
          <span className="up">{namespace}</span>
          <span className="sep">/</span>
          <span className="cur">{workload}</span>
          <span className="sep">·</span>
          <span className="up">shell</span>
        </div>
        <span className="spacer" />
        {pods.length > 1 && (
          <select
            className="field"
            value={pod}
            onChange={(e) => setPod(e.target.value)}
            title="which replica to open a shell in"
          >
            {pods.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
          </select>
        )}
      </div>

      <div className="page" style={{ display: "flex", flexDirection: "column", minHeight: 0 }}>
        {note && (
          <div className="banner" style={{ marginBottom: "var(--s3)" }}>
            <span className="st st-warn"><i className="glyph" /></span>
            <span>{note}</span>
          </div>
        )}
        <div
          ref={mount}
          style={{ flex: 1, minHeight: 320, border: "1px solid var(--line)", borderRadius: "var(--r2)", padding: "var(--s2)" }}
        />
        <p style={{ color: "var(--fg-4)", fontSize: "var(--fs-label)", marginTop: "var(--s3)" }}>
          This session is on the timeline. A shell in production is an event, not a page view.
        </p>
      </div>
    </>
  );
}

import { useEffect, useMemo, useRef, useState } from "react";
import type { ContextView } from "./api";
import { envToken } from "./health";
import { useLogs, type LogEdge, type LogLine } from "./stream";
import { bufferText, compileFilter, downloadName, interleave, level, podColor, renderText, shortPod } from "./logs";

// Screen 4. The whole workload, every replica merged and time-ordered, with a
// colour key per pod. Per-pod and per-container are filters on that stream, not
// a different way in: one tab per replica is the thing this screen exists to
// replace.

export function LogsScreen({
  context,
  namespace,
  workload,
  onBack,
}: {
  context: ContextView;
  namespace: string;
  workload: string;
  onBack: () => void;
}) {
  const [pattern, setPattern] = useState("");
  const [includeSidecars, setIncludeSidecars] = useState(false);
  const [includeInit, setIncludeInit] = useState(false);
  const [previous, setPrevious] = useState(false);
  const [hiddenPods, setHiddenPods] = useState<Set<string>>(new Set());
  const [following, setFollowing] = useState(true);

  const { lines, edges, dropped, error, liveness } = useLogs({
    context: context.name, namespace, workload, includeSidecars, includeInit, previous,
  });

  const filter = useMemo(() => compileFilter(pattern), [pattern]);

  // The pod key is built from what actually arrived, so a replica that never
  // logged does not claim a colour, and colours stay put while you read.
  const pods = useMemo(() => {
    const seen: string[] = [];
    for (const l of lines) if (!seen.includes(l.pod)) seen.push(l.pod);
    return seen.sort();
  }, [lines]);

  const shown = useMemo(() => {
    return lines.filter((l) => {
      if (hiddenPods.has(l.pod)) return false;
      if (filter.re && !new RegExp(filter.re.source, "i").test(l.text)) return false;
      return true;
    });
  }, [lines, hiddenPods, filter]);

  const env = envToken(context);

  return (
    <>
      <div className={`topbar env-edge${context.hazard ? " hazard" : ""}`} data-env={env}>
        <span className="env-chip">{context.environment}</span>
        <div className="crumb">
          <button className="tab" style={{ padding: 0 }} onClick={onBack}>apps</button>
          <span className="sep">/</span>
          <span className="up">{namespace}</span>
          <span className="sep">/</span>
          <span className="cur">{workload}</span>
        </div>
        <span className="spacer" />
        <button className="btn" onClick={() => copyPermalink(context.name, namespace, workload)}>
          Copy permalink
        </button>
        <button
          className="btn"
          onClick={() => download(bufferText(shown), downloadName(context.name, namespace, workload))}
          disabled={shown.length === 0}
        >
          Download buffer
        </button>
      </div>

      <div className="page" style={{ display: "flex", flexDirection: "column", paddingBottom: 0, minHeight: 0 }}>
        <div className="tabs">
          <span className="tab sel">Logs</span>
        </div>

        <div className="row" style={{ marginBottom: "var(--s3)" }}>
          <input
            className="field"
            style={{ minWidth: 260 }}
            placeholder="filter (regex)"
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
          />
          <button
            className={following ? "btn btn-primary" : "btn"}
            onClick={() => setFollowing((f) => !f)}
            aria-pressed={following}
          >
            {following ? "Following" : "Paused"}
          </button>
          <button className={includeSidecars ? "btn btn-primary" : "btn"} onClick={() => setIncludeSidecars((v) => !v)}>
            Sidecars
          </button>
          <button className={includeInit ? "btn btn-primary" : "btn"} onClick={() => setIncludeInit((v) => !v)}>
            Init containers
          </button>
          <button className={previous ? "btn btn-primary" : "btn"} onClick={() => setPrevious((v) => !v)}>
            Previous container
          </button>
          <span className="spacer" />
          <Counts shown={shown.length} total={lines.length} dropped={dropped} liveness={liveness} />
        </div>

        {filter.error && (
          <div className="row" style={{ marginBottom: "var(--s3)", color: "var(--warn)", fontSize: 11 }}>
            filter is not valid: {filter.error}. Showing everything.
          </div>
        )}

        <div
          className="row"
          style={{ marginBottom: "var(--s3)", paddingBottom: "var(--s3)", borderBottom: "1px solid var(--line)" }}
        >
          <PodKey pods={pods} hidden={hiddenPods} onToggle={(p) => setHiddenPods(toggle(hiddenPods, p))} />
          <span className="spacer" />
          <span style={{ color: "var(--fg-4)", fontSize: 11 }}>
            {includeSidecars ? "sidecars shown" : "mesh sidecars hidden"}
            {includeInit ? " · init containers shown" : " · init containers hidden"}
          </span>
        </div>

        <Body lines={shown} edges={edges} error={error} filter={filter.re} pods={pods} following={following} />
      </div>
    </>
  );
}

function Body({
  lines, edges, error, filter, pods, following,
}: {
  lines: LogLine[];
  edges: LogEdge[];
  error: string | null;
  filter: RegExp | null;
  pods: string[];
  following: boolean;
}) {
  const box = useRef<HTMLDivElement>(null);

  // Follow mode pins the view to the newest line. Pausing is what a developer
  // does to read something, so it must actually hold position.
  useEffect(() => {
    if (following && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, following]);

  if (error) {
    return (
      <div className="empty">
        <div className="head">No logs for this workload</div>
        <div className="mono" style={{ fontSize: 12 }}>{error}</div>
      </div>
    );
  }

  const items = interleave(lines, edges);
  const first = lines.find((l) => l.time)?.time;

  return (
    <div className="log" ref={box} style={{ flex: 1, overflow: "auto", paddingBottom: "var(--s5)" }}>
      {lines.length === 0 && edges.length === 0 && (
        <div style={{ color: "var(--fg-3)", padding: "var(--s4) 0" }}>
          <span className="spinner" /> waiting for output. A workload can be healthy and quiet.
        </div>
      )}

      {items.map((it, i) =>
        it.kind === "edge" ? (
          <Gap key={`e${i}`} edge={it.edge} before={it.edge.kind === "horizon" ? first : undefined} />
        ) : (
          <div className="log-row" key={it.line.seq}>
            <span className="ts">{clock(it.line.time)}</span>
            <span className="pod" style={{ color: podColor(it.line.pod, pods) }} title={it.line.pod}>
              {shortPod(it.line.pod)}
            </span>
            <span className={`msg ${lvl(it.line.text)}`}>
              {renderText(it.line.text, filter).map((p, j) =>
                p.hit ? <span className="hit" key={j}>{p.text}</span> : <span key={j}>{p.text}</span>,
              )}
              {it.line.truncated && <span className="tag" style={{ marginLeft: 6 }}>truncated</span>}
              {/* The line is in its right place; the tag says it reached us
                  after its moment, a fact about the stream, not the order. */}
              {it.line.late && <span className="tag" style={{ marginLeft: 6 }}>delayed</span>}
            </span>
          </div>
        ),
      )}
    </div>
  );
}

// An availability edge is a rule with a reason, placed where it happened.
// Blank space would read as a quiet period, and a crash loop is not quiet.
function Gap({ edge, before }: { edge: LogEdge; before?: string }) {
  return (
    <div className="log-gap" title={edge.pod}>
      {edge.pod && edge.kind !== "horizon" ? `${shortPod(edge.pod)} · ` : ""}
      {before ? `before ${clock(before)} · ` : ""}
      {edge.reason}
    </div>
  );
}

function PodKey({
  pods, hidden, onToggle,
}: {
  pods: string[];
  hidden: Set<string>;
  onToggle: (pod: string) => void;
}) {
  if (pods.length === 0) return <span style={{ color: "var(--fg-4)", fontSize: 11 }}>no replicas have logged yet</span>;
  return (
    <div className="pod-key">
      {pods.map((p) => (
        <span key={p}>
          <button
            onClick={() => onToggle(p)}
            aria-pressed={!hidden.has(p)}
            title={hidden.has(p) ? `${p} (hidden)` : p}
          >
            <i style={{ background: podColor(p, pods) }} />
            {shortPod(p)}
          </button>
        </span>
      ))}
    </div>
  );
}

function Counts({
  shown, total, dropped, liveness,
}: {
  shown: number;
  total: number;
  dropped: number;
  liveness: string;
}) {
  return (
    <span className="page-sub mono" style={{ fontSize: 11 }}>
      {shown === total ? `${total} lines` : `${shown} of ${total} lines`}
      {/* A buffer that quietly loses lines makes a chatty workload look calm. */}
      {dropped > 0 && ` · ${dropped} dropped by the buffer`}
      {liveness !== "live" && ` · ${liveness}`}
    </span>
  );
}

function lvl(text: string): string {
  const l = level(text);
  return l === "err" ? "lvl-err" : l === "warn" ? "lvl-warn" : "";
}

// clock keeps the time column narrow: the date is the same for everything on
// screen, and the milliseconds are what order a merge.
function clock(time?: string): string {
  if (!time) return "—".padEnd(12);
  return time.length >= 23 ? time.slice(11, 23) : time.slice(11, 19);
}

function toggle(set: Set<string>, v: string): Set<string> {
  const next = new Set(set);
  if (next.has(v)) next.delete(v);
  else next.add(v);
  return next;
}

// The permalink carries the workload, not the session token: a link shared with
// a teammate opens their kubeside against their own cluster.
function copyPermalink(context: string, namespace: string, workload: string) {
  const url = `${window.location.origin}${window.location.pathname}#logs/${encodeURIComponent(context)}/${encodeURIComponent(namespace)}/${encodeURIComponent(workload)}`;
  void navigator.clipboard?.writeText(url);
}

function download(text: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

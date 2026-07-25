// Client-side log handling: fold batches into a bounded list, compile the
// filter, and decide what colour a pod is.
//
// The server holds the authoritative 10k-line ring. This keeps a shorter one so
// a tab left open on a chatty workload cannot grow the DOM without bound.

import type { LogEdge, LogLine, LogsBatch } from "./stream";

/** CLIENT_LINES is the on-screen ring. Deep enough to scroll, short enough to render. */
export const CLIENT_LINES = 5000;

// applyBatch folds one batch into the current list.
//
// Lines carry a server sequence, so a batch a joining window sees twice costs a
// lookup rather than a duplicated line. A reset batch replaces everything,
// which is what a fresh subscription and a reconnect both send.
export function applyBatch(current: LogLine[], batch: LogsBatch, cap = CLIENT_LINES): LogLine[] {
  const base = batch.reset ? [] : current;
  const incoming = batch.lines ?? [];
  if (incoming.length === 0) return base === current ? current : base;

  const seen = new Set(base.map((l) => l.seq));
  const fresh = incoming.filter((l) => !seen.has(l.seq));
  // Time is the merge key, sequence only the tiebreaker. A line the server had
  // to insert behind others still lands where it belongs on screen.
  const next = [...base, ...fresh].sort((a, b) => at(a.time) - at(b.time) || a.seq - b.seq);

  return next.length > cap ? next.slice(next.length - cap) : next;
}

// at parses a wire timestamp to milliseconds. Comparing the strings would be
// wrong twice over: zones differ, and RFC3339 trims trailing zeros from the
// fraction, so "10:00:00.5Z" sorts before "10:00:00Z" as text.
function at(time?: string): number {
  const v = time ? Date.parse(time) : NaN;
  return Number.isNaN(v) ? 0 : v;
}

export interface Filter {
  re: RegExp | null;
  error: string | null;
}

// compileFilter turns the filter box into a regex. A half-typed pattern is the
// normal state of that box, so a broken one reports itself instead of throwing
// and blanking the screen.
export function compileFilter(pattern: string): Filter {
  const p = pattern.trim();
  if (!p) return { re: null, error: null };
  try {
    return { re: new RegExp(p, "gi"), error: null };
  } catch (e) {
    return { re: null, error: e instanceof Error ? e.message : String(e) };
  }
}

// The pod palette. Colour here identifies a replica and nothing else: it never
// carries status, which is the status channel's job.
const POD_COLORS = [
  "#E5484D", "#4C8DD9", "#4FB286", "#E0A72E", "#9B6FD6", "#8B9BA3",
  "#D97757", "#3FA6C4", "#7BA83F", "#C77DBB",
];

// podColor is stable for a pod as long as the pod set is: the same replica
// keeps its colour while you read, which is the whole point of the key.
export function podColor(pod: string, pods: string[]): string {
  const i = pods.indexOf(pod);
  return POD_COLORS[(i < 0 ? 0 : i) % POD_COLORS.length];
}

// shortPod keeps the replica suffix, which is the part that tells six pods of
// one deployment apart.
export function shortPod(pod: string): string {
  const parts = pod.split("-");
  return parts.length > 1 ? parts[parts.length - 1] : pod;
}

const ERR = /\b(error|err|fatal|panic|exception|fail(ed|ure)?)\b|\bhttp 5\d\d\b|\b5\d\d [A-Z]{3,7} \//i;
const WARN = /\b(warn|warning|slow|retry|degraded|timeout)\b/i;

// level classifies a line for the colour channel. Word boundaries matter: if
// every line containing "error" anywhere lit up, the channel would say nothing.
export function level(text: string): "err" | "warn" | "" {
  if (ERR.test(text)) return "err";
  if (WARN.test(text)) return "warn";
  return "";
}

export interface Part {
  text: string;
  hit: boolean;
}

// renderText splits a line around filter matches so they can be highlighted.
export function renderText(text: string, re: RegExp | null): Part[] {
  if (!re) return [{ text, hit: false }];

  const parts: Part[] = [];
  const rx = new RegExp(re.source, re.flags.includes("g") ? re.flags : re.flags + "g");
  let last = 0;
  for (const m of text.matchAll(rx)) {
    // A zero-width match would otherwise loop forever without advancing.
    if (m[0] === "" || m.index === undefined) continue;
    if (m.index > last) parts.push({ text: text.slice(last, m.index), hit: false });
    parts.push({ text: m[0], hit: true });
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push({ text: text.slice(last), hit: false });
  return parts.length > 0 ? parts : [{ text, hit: false }];
}

export function downloadName(context: string, namespace: string, workload: string): string {
  return `kubeside-${context}-${namespace}-${workload}.log`;
}

// bufferText renders the visible buffer for download, keeping the pod column so
// a saved file still says which replica said what.
export function bufferText(lines: LogLine[]): string {
  return lines.map((l) => `${l.time ?? ""} ${l.pod} ${l.container} ${l.text}`).join("\n") + "\n";
}

/** An item in the rendered stream: a log line, or an edge in what is knowable. */
export type Item =
  | { kind: "line"; line: LogLine }
  | { kind: "edge"; edge: LogEdge };

// interleave places availability edges among the lines by time.
//
// An edge floated to the bottom of a scrollback says nothing useful: what a
// developer needs is the boundary at the point in the stream where it happened.
// The horizon is the exception and pins to the top, because it describes
// everything before the first line rather than a moment inside the stream.
export function interleave(lines: LogLine[], edges: LogEdge[]): Item[] {
  const horizon = edges.filter((e) => e.kind === "horizon" || !e.time);
  const placed = edges.filter((e) => e.kind !== "horizon" && e.time);

  const items: { at: number; item: Item }[] = [
    ...lines.map((l) => ({ at: at(l.time), item: { kind: "line", line: l } as Item })),
    ...placed.map((e) => ({ at: at(e.time), item: { kind: "edge", edge: e } as Item })),
  ];
  items.sort((a, b) => a.at - b.at);

  // One horizon rule, not one per replica: six pods that all started before the
  // window would otherwise print the same sentence six times.
  const head: Item[] = horizon.length > 0 ? [{ kind: "edge", edge: horizon[0] }] : [];
  return [...head, ...items.map((i) => i.item)];
}

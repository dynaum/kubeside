// Laying out a timeline: turning timestamps into positions on a shared axis,
// and grouping entries into the lanes the design defines.

import type { TimelineEntry, TimelineHorizon } from "./api";

/** Lane is one row of the axis. */
export interface Lane {
  key: string;
  label: string;
  kinds: string[];
  /** mark is the CSS class that shapes this lane's marks. */
  mark: string;
}

// Lanes follow the design: a reader scanning for "when did it last deploy"
// should not have to read every entry to find the deploys.
export const LANES: Lane[] = [
  { key: "deploy", label: "Deploys", kinds: ["deploy"], mark: "deploy" },
  { key: "release", label: "Helm", kinds: ["release"], mark: "cfg" },
  { key: "health", label: "Health", kinds: ["health"], mark: "run-ok" },
  { key: "restart", label: "Restarts", kinds: ["restart", "warning"], mark: "restart" },
];

/** Span is the window the axis covers. */
export interface Span {
  from: number;
  to: number;
}

// span is the window the axis covers: the oldest thing worth showing to now.
// A fixed window would either cut off history or leave the recent hours
// squeezed into a sliver, and the recent hours are what an incident is about.
export function span(entries: TimelineEntry[], horizons: TimelineHorizon[], now: number): Span {
  const times = [
    ...entries.map((e) => Date.parse(e.at)),
    ...horizons.map((h) => Date.parse(h.at)),
  ].filter((t) => Number.isFinite(t));

  if (times.length === 0) return { from: now - 3600_000, to: now };

  const from = Math.min(...times);
  // A window with no width would put every mark at the same position.
  return { from: from === now ? now - 60_000 : from, to: now };
}

/** at returns the 0..100 position of a moment on the axis, clamped. */
export function position(when: string | number, s: Span): number {
  const t = typeof when === "number" ? when : Date.parse(when);
  if (!Number.isFinite(t) || s.to <= s.from) return 0;
  const pct = ((t - s.from) / (s.to - s.from)) * 100;
  return Math.max(0, Math.min(100, pct));
}

/** laneEntries selects the entries belonging to one lane. */
export function laneEntries(entries: TimelineEntry[], lane: Lane): TimelineEntry[] {
  return entries.filter((e) => lane.kinds.includes(e.kind));
}

// ticks are the labels under the axis. Five of them: enough to read a date
// against, few enough not to become a ruler.
export function ticks(s: Span, count = 5): string[] {
  const out: string[] = [];
  for (let i = 0; i < count; i++) {
    const t = s.from + ((s.to - s.from) * i) / (count - 1);
    out.push(i === count - 1 ? "now" : shortTime(t, s));
  }
  return out;
}

// shortTime shows the clock inside a day and the date beyond it, because "14:22"
// is meaningless three days back and "20 Jul" is meaningless during an incident.
function shortTime(t: number, s: Span): string {
  const d = new Date(t);
  const spanMs = s.to - s.from;
  if (spanMs <= 36 * 3600_000) {
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  return `${pad(d.getDate())} ${d.toLocaleString(undefined, { month: "short" })}`;
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** age renders a duration the way an operator says it: 11d, 4h, 12m. */
export function age(seconds: number): string {
  if (seconds <= 0) return "—";
  const d = Math.floor(seconds / 86400);
  if (d > 0) return `${d}d`;
  const h = Math.floor(seconds / 3600);
  if (h > 0) return `${h}h`;
  const m = Math.floor(seconds / 60);
  if (m > 0) return `${m}m`;
  return `${Math.floor(seconds)}s`;
}

/** clock renders a wall time for the changes table. */
export function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const today = new Date();
  const sameDay = d.toDateString() === today.toDateString();
  return sameDay
    ? `${pad(d.getHours())}:${pad(d.getMinutes())}`
    : `${pad(d.getDate())} ${d.toLocaleString(undefined, { month: "short" })}`;
}

/** Marker is a horizon rendered on one lane: where knowledge ends, and why. */
export interface Marker {
  at: number;
  /** caption is the short label on the axis; reason is the full sentence. */
  caption: string;
  reason: string;
  /** unknownPct is how much of the lane, from the left, is not knowable. */
  unknownPct: number;
}

// horizonSource maps a lane to the reconstruction source whose horizon bounds
// it. A lane without one is bounded by nothing and says so differently.
const LANE_HORIZON: Record<string, string[]> = {
  deploy: ["replicaset", "controllerrevision"],
  release: ["helm"],
  health: ["session"],
  restart: ["event"],
};

// caption is the short label the axis shows. The full reason travels in the
// tooltip, because the axis has room for four words and the reason is a
// sentence.
function caption(h: TimelineHorizon): string {
  if (h.source === "session") {
    return h.pruned ? "session buffer evicted" : "kubeside started here";
  }
  if (h.source === "event") return "events expire";
  return h.pruned ? "reconstruction ends" : "first revision";
}

// marker computes what to draw on one lane.
//
// The hatched region left of the marker is the point: an empty axis with
// nothing on it reads as a quiet period, and a quiet period is the one thing
// this axis must never imply when the truth is "not known".
export function marker(horizons: TimelineHorizon[], lane: Lane, s: Span): Marker | null {
  const sources = LANE_HORIZON[lane.key] ?? [];
  const h = horizons.find((x) => sources.includes(x.source));
  if (!h) return null;

  const pct = position(h.at, s);
  return { at: Date.parse(h.at), caption: caption(h), reason: h.reason, unknownPct: pct };
}

// A lane with no horizon and no entries is entirely unknown: nothing was read,
// nothing is claimed. Hatching the whole track says that without a sentence.
export function fullyUnknown(entries: TimelineEntry[], m: Marker | null): boolean {
  return m === null && entries.length === 0;
}

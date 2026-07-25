// The command palette's logic: what commands exist, and which of them match
// what has been typed.
//
// Rafael arrives from k9s and judges the tool on keyboard operability within
// thirty seconds, so this is not a search box bolted onto a mouse-driven UI.
// Every navigation and action the product has is reachable here.

import type { AppView, ContextView } from "./api";
import type { Route } from "./route";

/** Command is one thing the palette can do. */
export interface Command {
  id: string;
  group: string;
  /** label is what is matched against and displayed. */
  label: string;
  hint?: string;
  /** health colours the glyph for app rows; absent for plain actions. */
  health?: string;
  route: Route;
}

export interface Match {
  command: Command;
  /** ranges are the matched character spans, for highlighting. */
  ranges: [number, number][];
  score: number;
}

// commands builds the list from what is on screen and what has been connected.
//
// Only environments already connected contribute their apps. Searching every
// context would wake every cluster in the kubeconfig, which is exactly what the
// connection model exists to avoid, so the palette respects it rather than
// quietly working around it.
export function commands(
  contexts: ContextView[],
  current: ContextView | null,
  apps: AppView[],
  route: Route,
): Command[] {
  const out: Command[] = [];

  if (current) {
    for (const a of apps) {
      out.push({
        id: `app:${current.name}:${a.namespace}/${a.name}`,
        group: "Apps",
        label: `${current.environment || current.name} / ${a.namespace} / ${a.name}`,
        hint: [a.ready, a.health].filter(Boolean).join(" · "),
        health: a.health,
        route: { screen: "app", context: current.name, namespace: a.namespace, workload: a.name },
      });
    }
  }

  // Actions apply to whatever the current screen is about. Offering "tail logs"
  // with nothing selected would be a command that cannot run.
  const target = routeTarget(route);
  if (current && target) {
    const { namespace, workload } = target;
    out.push(
      {
        id: "action:logs",
        group: "Actions",
        label: `Tail logs for ${workload}`,
        hint: "all replicas merged",
        route: { screen: "logs", context: current.name, namespace, workload },
      },
      {
        id: "action:config",
        group: "Actions",
        label: `Resolved configuration for ${workload}`,
        hint: "every source merged",
        route: { screen: "config", context: current.name, namespace, workload },
      },
      {
        id: "action:diff",
        group: "Actions",
        label: `Compare ${workload} across environments`,
        hint: "drift, suspicious, missing",
        route: { screen: "diff", context: current.name, namespace, workload },
      },
      {
        id: "action:timeline",
        group: "Actions",
        label: `Timeline for ${workload}`,
        hint: "what changed, and when",
        route: { screen: "app", context: current.name, namespace, workload },
      },
    );
  }

  // The promotion view is not about one environment, so it is always offered.
  out.push({
    id: "view:promotion",
    group: "Views",
    label: "Promotion matrix",
    hint: "is the fix in prod yet",
    route: { screen: "promotion" },
  });

  for (const c of contexts) {
    if (c.name === current?.name) continue;
    out.push({
      id: `env:${c.name}`,
      group: "Environments",
      label: `Switch to ${c.environment || c.name}`,
      hint: c.name,
      route: { screen: "apps", context: c.name },
    });
  }

  return out;
}

// routeTarget is the app the current screen is about, when it is about one.
function routeTarget(route: Route): { namespace: string; workload: string } | null {
  if (route.screen === "apps" || route.screen === "promotion") return null;
  return { namespace: route.namespace, workload: route.workload };
}

// search ranks commands against a query.
//
// Matching is subsequence-based, the way every palette a developer has used
// works: "pay" finds payments, and "tacheck" finds team-a / checkout. An empty
// query returns everything in its natural order, because the palette is also
// how somebody discovers what the tool can do.
export function search(list: Command[], query: string): Match[] {
  const q = query.trim().toLowerCase();
  if (!q) return list.map((command) => ({ command, ranges: [], score: 0 }));

  const out: Match[] = [];
  for (const command of list) {
    const m = matchOne(command.label, q);
    if (m) out.push({ command, ranges: m.ranges, score: m.score });
  }
  // A contiguous match beats a scattered one, and ties keep source order so
  // the list does not reshuffle as somebody types.
  return out.sort((a, b) => b.score - a.score);
}

function matchOne(label: string, q: string): { ranges: [number, number][]; score: number } | null {
  const haystack = label.toLowerCase();

  // A contiguous hit is what somebody usually means, so it is tried first and
  // scored highest.
  const at = haystack.indexOf(q);
  if (at >= 0) {
    const boundary = at === 0 || /[^a-z0-9]/.test(haystack[at - 1]);
    return { ranges: [[at, at + q.length]], score: 1000 + (boundary ? 100 : 0) - at };
  }

  const ranges: [number, number][] = [];
  let i = 0;
  let gaps = 0;
  let last = -2;
  for (const ch of q) {
    const found = haystack.indexOf(ch, i);
    if (found < 0) return null;
    if (found !== last + 1) gaps++;
    if (ranges.length > 0 && ranges[ranges.length - 1][1] === found) {
      ranges[ranges.length - 1][1] = found + 1;
    } else {
      ranges.push([found, found + 1]);
    }
    last = found;
    i = found + 1;
  }
  return { ranges, score: 100 - gaps * 10 - i };
}

/** groups splits matches into their display groups, preserving order. */
export function groups(matches: Match[]): { name: string; matches: Match[] }[] {
  const order: string[] = [];
  const byGroup = new Map<string, Match[]>();
  for (const m of matches) {
    const g = m.command.group;
    if (!byGroup.has(g)) {
      byGroup.set(g, []);
      order.push(g);
    }
    byGroup.get(g)!.push(m);
  }
  return order.map((name) => ({ name, matches: byGroup.get(name)! }));
}

/** move wraps the selection, because a list that stops at the end is one that
 * makes you reverse direction to reach the thing just past it. */
export function move(index: number, delta: number, length: number): number {
  if (length === 0) return 0;
  return (index + delta + length) % length;
}

/** split turns a label plus its matched ranges into renderable parts. */
export function split(label: string, ranges: [number, number][]): { text: string; hit: boolean }[] {
  if (ranges.length === 0) return [{ text: label, hit: false }];
  const parts: { text: string; hit: boolean }[] = [];
  let at = 0;
  for (const [from, to] of ranges) {
    if (from > at) parts.push({ text: label.slice(at, from), hit: false });
    parts.push({ text: label.slice(from, to), hit: true });
    at = to;
  }
  if (at < label.length) parts.push({ text: label.slice(at), hit: false });
  return parts;
}

import { describe, expect, it } from "vitest";
import { age, clock, fullyUnknown, laneEntries, LANES, marker, position, span, ticks } from "./detail";
import type { TimelineEntry, TimelineHorizon } from "./api";

function entry(at: string, kind: string): TimelineEntry {
  return { at, kind, title: kind, source: "test" };
}

const NOW = Date.parse("2026-07-24T12:00:00Z");

describe("span", () => {
  it("runs from the oldest known moment to now", () => {
    const s = span([entry("2026-07-24T10:00:00Z", "deploy")], [], NOW);
    expect(s.from).toBe(Date.parse("2026-07-24T10:00:00Z"));
    expect(s.to).toBe(NOW);
  });

  // A horizon is older than every entry by definition, and it has to fit on
  // the axis or the marker it carries lands off-screen.
  it("includes horizons, which sit before the entries", () => {
    const s = span(
      [entry("2026-07-24T11:00:00Z", "deploy")],
      [{ at: "2026-07-20T00:00:00Z", source: "replicaset", reason: "pruned", pruned: true }],
      NOW,
    );
    expect(s.from).toBe(Date.parse("2026-07-20T00:00:00Z"));
  });

  it("falls back to the last hour when nothing is known", () => {
    const s = span([], [], NOW);
    expect(s.to - s.from).toBe(3600_000);
  });

  // A zero-width window would stack every mark at the same position.
  it("never produces a window of no width", () => {
    const s = span([entry(new Date(NOW).toISOString(), "deploy")], [], NOW);
    expect(s.to).toBeGreaterThan(s.from);
  });
});

describe("position", () => {
  const s = { from: 0, to: 100_000 };

  it("maps a moment to a percentage of the axis", () => {
    expect(position(50_000, s)).toBe(50);
    expect(position(0, s)).toBe(0);
    expect(position(100_000, s)).toBe(100);
  });

  // A mark before the horizon belongs at the edge, not off the track.
  it("clamps anything outside the window", () => {
    expect(position(-5000, s)).toBe(0);
    expect(position(500_000, s)).toBe(100);
  });

  it("survives an unparseable timestamp", () => {
    expect(position("not a time", s)).toBe(0);
  });
});

describe("lanes", () => {
  it("puts each kind in exactly one lane", () => {
    const kinds = LANES.flatMap((l) => l.kinds);
    expect(new Set(kinds).size).toBe(kinds.length);
  });

  it("selects only its own entries", () => {
    const entries = [entry("2026-07-24T10:00:00Z", "deploy"), entry("2026-07-24T11:00:00Z", "restart")];
    const deploys = laneEntries(entries, LANES[0]);
    expect(deploys).toHaveLength(1);
    expect(deploys[0].kind).toBe("deploy");
  });
});

describe("ticks", () => {
  it("ends at now, because that is the edge a reader looks from", () => {
    const t = ticks({ from: NOW - 7 * 86400_000, to: NOW });
    expect(t).toHaveLength(5);
    expect(t[4]).toBe("now");
  });

  // "14:22" is meaningless three days back; "20 Jul" is meaningless during an
  // incident. The window decides which one is useful.
  it("shows clock times inside a day and dates beyond one", () => {
    const short = ticks({ from: NOW - 6 * 3600_000, to: NOW });
    expect(short[0]).toMatch(/^\d\d:\d\d$/);
    const long = ticks({ from: NOW - 14 * 86400_000, to: NOW });
    expect(long[0]).toMatch(/^\d\d \w+$/);
  });
});

describe("age", () => {
  it("reads the way an operator says it", () => {
    expect(age(11 * 86400)).toBe("11d");
    expect(age(4 * 3600)).toBe("4h");
    expect(age(90)).toBe("1m");
    expect(age(5)).toBe("5s");
  });

  it("says nothing rather than zero when the age is unknown", () => {
    expect(age(0)).toBe("—");
  });
});

describe("clock", () => {
  it("survives a timestamp it cannot parse", () => {
    expect(clock("nonsense")).toBe("—");
  });
});

describe("marker", () => {
  const s = { from: Date.parse("2026-07-20T00:00:00Z"), to: NOW };

  function horizon(source: string, at: string, pruned: boolean, reason = "because"): TimelineHorizon {
    return { source, at, pruned, reason };
  }

  // Where reconstruction ran out, labeled with the cause. Mandatory, per the
  // spec, and never decorative.
  it("marks where reconstruction ends on the deploy lane", () => {
    const m = marker([horizon("replicaset", "2026-07-22T00:00:00Z", true)], LANES[0], s);
    expect(m?.caption).toBe("reconstruction ends");
    expect(m?.unknownPct).toBeGreaterThan(0);
    expect(m?.unknownPct).toBeLessThan(100);
  });

  // Nothing was pruned: this is the app's beginning, not a cut, and saying
  // "reconstruction ends" would be a lie in the other direction.
  it("distinguishes a first revision from a cut", () => {
    const m = marker([horizon("replicaset", "2026-07-22T00:00:00Z", false)], LANES[0], s);
    expect(m?.caption).toBe("first revision");
  });

  // The other mandatory marker.
  it("marks where the session began", () => {
    const m = marker([horizon("session", "2026-07-24T11:00:00Z", false)], LANES[2], s);
    expect(m?.caption).toBe("kubeside started here");
  });

  it("says so when the session buffer evicted", () => {
    const m = marker([horizon("session", "2026-07-24T11:00:00Z", true)], LANES[2], s);
    expect(m?.caption).toBe("session buffer evicted");
  });

  it("names the apiserver TTL on the restart lane", () => {
    const m = marker([horizon("event", "2026-07-24T11:00:00Z", true)], LANES[3], s);
    expect(m?.caption).toBe("events expire");
  });

  it("keeps the full reason for the tooltip", () => {
    const m = marker([horizon("replicaset", "2026-07-22T00:00:00Z", true, "pruned by revisionHistoryLimit")], LANES[0], s);
    expect(m?.reason).toBe("pruned by revisionHistoryLimit");
  });

  it("has no marker for a lane whose source reported none", () => {
    expect(marker([horizon("event", "2026-07-24T11:00:00Z", true)], LANES[0], s)).toBeNull();
  });
});

describe("fullyUnknown", () => {
  // A lane with no horizon and no entries knows nothing. Hatching the whole
  // track says that; an empty line would claim a quiet period.
  it("is true for a lane with no horizon and no entries", () => {
    expect(fullyUnknown([], null)).toBe(true);
  });

  it("is false once anything is known", () => {
    expect(fullyUnknown([entry("2026-07-24T10:00:00Z", "deploy")], null)).toBe(false);
    expect(fullyUnknown([], { at: 0, caption: "c", reason: "r", unknownPct: 10 })).toBe(false);
  });
});

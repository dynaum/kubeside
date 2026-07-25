import { describe, expect, it } from "vitest";
import { age, clock, laneEntries, LANES, position, span, ticks } from "./detail";
import type { TimelineEntry } from "./api";

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

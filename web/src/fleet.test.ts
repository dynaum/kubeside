import { describe, expect, it } from "vitest";
import { notPresentLabel, readySplit, verdictBadge } from "./fleet";

// These three functions were stranded inside FleetScreen.tsx: the vitest
// include in vite.config.ts does not match .tsx, and there is no jsdom in
// this package, so nothing reached from the component was ever reachable
// from a test. This file is new coverage of behavior that already shipped
// correctly; the extraction did not change what renders.

describe("notPresentLabel", () => {
  it("names the three ways a row has no version", () => {
    expect(notPresentLabel("unreachable")).toBe("no answer");
    expect(notPresentLabel("denied")).toBe("no access");
    expect(notPresentLabel("pending")).toBe("asking");
  });

  // A blank cell here would read as "checked and found nothing". A state
  // nobody wrote into the map must never produce that lie, so it falls back
  // to naming itself.
  it("surfaces an unknown state as itself, never blank", () => {
    expect(notPresentLabel("quarantined")).toBe("quarantined");
    expect(notPresentLabel("")).toBe("");
  });
});

describe("readySplit", () => {
  it("splits a ratio at the slash", () => {
    expect(readySplit("4/4")).toEqual({ whole: "4", of: "/4" });
  });

  it("keeps a value with no separator whole", () => {
    expect(readySplit("asking")).toEqual({ whole: "asking", of: null });
  });

  it("keeps an empty string whole rather than slicing it", () => {
    expect(readySplit("")).toEqual({ whole: "", of: null });
  });
});

describe("verdictBadge", () => {
  // One tag resolving to two digests is the worse fact: two clusters claiming
  // this version are not running the same code. Showing "behind" alongside it
  // would suggest two independent problems, so mutableTag wins outright.
  it("suppresses the behind badge when the tag is mutable", () => {
    expect(verdictBadge({ mutableTag: true, behind: true })).toBe("mutable-tag");
  });

  it("shows behind on its own", () => {
    expect(verdictBadge({ mutableTag: false, behind: true })).toBe("behind");
  });

  it("shows no badge when neither applies", () => {
    expect(verdictBadge({ mutableTag: false, behind: false })).toBeNull();
    expect(verdictBadge({})).toBeNull();
  });
});

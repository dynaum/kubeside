import { describe, expect, it } from "vitest";
import { restartCell, revisionAge, tagCell } from "./rows";

describe("tagCell", () => {
  it("shows the tag", () => {
    expect(tagCell("1.4.2")).toBe("1.4.2");
  });

  // A digest-pinned or metadata-only read has no tag. A dash says so; anything
  // else would be a claim about which version is live.
  it("shows a dash when no tag was read", () => {
    expect(tagCell(undefined)).toBe("—");
    expect(tagCell("")).toBe("—");
  });
});

describe("revisionAge", () => {
  const now = Date.parse("2026-08-01T12:00:00Z");

  it("measures from the moment the revision appeared", () => {
    expect(revisionAge("2026-08-01T10:30:00Z", now)).toBe("1h");
    expect(revisionAge("2026-07-20T12:00:00Z", now)).toBe("12d");
  });

  it("shows a dash when nothing carried a creation stamp", () => {
    expect(revisionAge(undefined, now)).toBe("—");
    expect(revisionAge("", now)).toBe("—");
  });

  // Clock skew between the laptop and the cluster can put a revision a few
  // seconds in the future. "0s" is honest; a negative duration is not.
  it("does not render a negative age", () => {
    expect(revisionAge("2026-08-01T12:00:30Z", now)).toBe("0s");
  });

  it("shows a dash for an unparseable stamp", () => {
    expect(revisionAge("not a time", now)).toBe("—");
  });
});

describe("restartCell", () => {
  it("shows the count when pods were read", () => {
    expect(restartCell(3, 7)).toEqual({ text: "7", warn: true });
    expect(restartCell(3, 0)).toEqual({ text: "0", warn: false });
  });

  // Zero restarts across zero pods is a reading nobody took, not a calm app.
  it("shows a dash when no pods were read", () => {
    expect(restartCell(0, 0)).toEqual({ text: "—", warn: false });
  });
});

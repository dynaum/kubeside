import { describe, expect, it } from "vitest";
import { cpuCell, memCell, restartCell, revisionAge, tagCell, usageNote } from "./rows";

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

describe("cpuCell", () => {
  // Millicores, the same unit kubectl top prints, so a developer comparing the
  // two screens is comparing the same number.
  it("reads in millicores", () => {
    expect(cpuCell(2, 120)).toBe("120m");
    expect(cpuCell(2, 1200)).toBe("1200m");
  });

  // A measured zero is a reading: the app is idle. It has to look different
  // from an app nobody measured.
  it("shows a measured zero", () => {
    expect(cpuCell(1, 0)).toBe("0m");
  });

  it("shows a dash when nothing reported", () => {
    expect(cpuCell(0, 0)).toBe("—");
  });
});

describe("memCell", () => {
  it("reads in the unit that fits", () => {
    expect(memCell(2, 384 * 1024 * 1024)).toBe("384Mi");
    expect(memCell(2, 2 * 1024 * 1024 * 1024)).toBe("2.0Gi");
    expect(memCell(1, 900 * 1024)).toBe("1Mi");
  });

  it("shows a measured zero", () => {
    expect(memCell(1, 0)).toBe("0Mi");
  });

  it("shows a dash when nothing reported", () => {
    expect(memCell(0, 0)).toBe("—");
  });
});

describe("usageNote", () => {
  // A total built from three of four replicas is not the app's usage. Saying so
  // is the difference between a number and a number you can act on.
  it("names a partial reading", () => {
    expect(usageNote(3, 4)).toBe("3 of 4 pods reported");
  });

  it("says nothing when every pod reported", () => {
    expect(usageNote(4, 4)).toBe("");
    expect(usageNote(0, 0)).toBe("");
  });
});

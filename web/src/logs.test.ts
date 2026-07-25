import { describe, expect, it } from "vitest";
import { applyBatch, compileFilter, downloadName, level, podColor, renderText, shortPod } from "./logs";
import type { LogLine } from "./stream";

function line(seq: number, pod: string, text: string, time = "2026-07-24T10:00:00Z"): LogLine {
  return { seq, pod, container: "app", text, time };
}

describe("applyBatch", () => {
  it("appends new lines in sequence order", () => {
    const next = applyBatch([], { lines: [line(2, "a", "two"), line(1, "a", "one")] });
    expect(next.map((l) => l.text)).toEqual(["one", "two"]);
  });

  // A window joining mid-flush may see one batch twice. The sequence makes
  // that a lookup, not a duplicated line on screen.
  it("drops lines it already has", () => {
    const first = applyBatch([], { lines: [line(1, "a", "one")] });
    const next = applyBatch(first, { lines: [line(1, "a", "one"), line(2, "a", "two")] });
    expect(next.map((l) => l.text)).toEqual(["one", "two"]);
  });

  // Replicas answer at different speeds, so an older line can arrive with a
  // newer sequence. What the developer reads is real time order.
  it("orders by timestamp, not by arrival", () => {
    const next = applyBatch([], {
      lines: [
        line(1, "a", "later", "2026-07-24T10:00:05Z"),
        line(2, "b", "earlier", "2026-07-24T10:00:01Z"),
      ],
    });
    expect(next.map((l) => l.text)).toEqual(["earlier", "later"]);
  });

  it("replaces everything on a reset batch", () => {
    const first = applyBatch([], { lines: [line(1, "a", "stale")] });
    const next = applyBatch(first, { reset: true, lines: [line(9, "a", "fresh")] });
    expect(next.map((l) => l.text)).toEqual(["fresh"]);
  });

  // The client ring exists so a week of output cannot grow the tab without
  // bound. The server ring is the source of truth; this one just keeps the DOM
  // survivable.
  it("keeps at most the client cap, newest first out of the top", () => {
    let lines: LogLine[] = [];
    for (let i = 1; i <= 12; i++) lines = applyBatch(lines, { lines: [line(i, "a", `l${i}`)] }, 5);
    expect(lines).toHaveLength(5);
    expect(lines[0].text).toBe("l8");
    expect(lines[4].text).toBe("l12");
  });
});

describe("compileFilter", () => {
  it("compiles a regex", () => {
    const f = compileFilter("level=(error|warn)");
    expect(f.error).toBeNull();
    expect(f.re?.test("level=error here")).toBe(true);
    expect(f.re?.test("level=info here")).toBe(false);
  });

  // A half-typed regex is the normal state of a filter box. It must report the
  // problem, not throw and blank the screen.
  it("reports a broken pattern instead of throwing", () => {
    const f = compileFilter("level=(error");
    expect(f.re).toBeNull();
    expect(f.error).toBeTruthy();
  });

  it("treats an empty pattern as no filter", () => {
    expect(compileFilter("  ").re).toBeNull();
    expect(compileFilter("  ").error).toBeNull();
  });
});

describe("podColor", () => {
  it("is stable for a pod across calls", () => {
    const pods = ["checkout-1", "checkout-2", "checkout-3"];
    expect(podColor("checkout-2", pods)).toBe(podColor("checkout-2", pods));
  });

  it("gives different pods different colours", () => {
    const pods = ["a", "b", "c"];
    expect(new Set(pods.map((p) => podColor(p, pods))).size).toBe(3);
  });

  // More replicas than palette entries is normal at scale; the key must still
  // render rather than hand back undefined.
  it("wraps when there are more pods than colours", () => {
    const many = Array.from({ length: 40 }, (_, i) => `pod-${i}`);
    for (const p of many) expect(podColor(p, many)).toMatch(/^#/);
  });
});

describe("shortPod", () => {
  it("keeps the replica suffix, which is what tells pods apart", () => {
    expect(shortPod("checkout-7768db9df5-98s7t")).toBe("98s7t");
    expect(shortPod("checkout")).toBe("checkout");
  });
});

describe("level", () => {
  it("finds errors and warnings in ordinary log text", () => {
    expect(level("http 500 POST /v1/payments")).toBe("err");
    expect(level("level=error pool timeout")).toBe("err");
    expect(level("WARN pool.acquire slow")).toBe("warn");
    expect(level("payment.authorize ok")).toBe("");
  });

  // "warning" inside a word is not a level. Colouring every line that happens
  // to contain "error" would make the channel meaningless.
  it("does not match a substring inside a word", () => {
    expect(level("errorless run completed")).toBe("");
  });
});

describe("renderText", () => {
  it("splits a line into hits and misses for highlighting", () => {
    const parts = renderText("a MAX_CONN=25 b", /MAX_CONN=\d+/g);
    expect(parts).toEqual([
      { text: "a ", hit: false },
      { text: "MAX_CONN=25", hit: true },
      { text: " b", hit: false },
    ]);
  });

  it("returns one plain part when nothing matches", () => {
    expect(renderText("nothing here", /zzz/g)).toEqual([{ text: "nothing here", hit: false }]);
  });

  // A pattern that matches the empty string would otherwise spin forever.
  it("survives a zero-width match", () => {
    expect(renderText("abc", /x*/g)).toEqual([{ text: "abc", hit: false }]);
  });
});

describe("downloadName", () => {
  it("names the file after what it contains", () => {
    expect(downloadName("prod", "team-a", "checkout")).toBe("kubeside-prod-team-a-checkout.log");
  });
});

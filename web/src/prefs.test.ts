import { describe, expect, it } from "vitest";
import {
  DEFAULT, SCALES, apply, cycleTheme, read, resolveTheme, step, write,
} from "./prefs";

/** A localStorage stand-in. The real one is absent in some browsers and throws in others. */
function store(seed?: Record<string, string>) {
  const data = new Map(Object.entries(seed ?? {}));
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => { data.set(k, v); },
    removeItem: (k: string) => { data.delete(k); },
    get size() { return data.size; },
  };
}

/** A document element stand-in, so this stays a pure test with no jsdom. */
function root() {
  const attrs: Record<string, string> = {};
  const vars: Record<string, string> = {};
  return {
    attrs, vars,
    setAttribute: (k: string, v: string) => { attrs[k] = v; },
    style: { setProperty: (k: string, v: string) => { vars[k] = v; } },
  };
}

describe("read", () => {
  it("defaults to following the system at normal size", () => {
    expect(read(store())).toEqual(DEFAULT);
    expect(DEFAULT).toEqual({ theme: "system", scale: 1 });
  });

  it("reads what was written", () => {
    const s = store();
    write(s, { theme: "light", scale: 1.25 });
    expect(read(s)).toEqual({ theme: "light", scale: 1.25 });
  });

  // Storage is shared with whatever else runs on 127.0.0.1, and a preference
  // that throws on read would take the whole UI down with it.
  it("treats unparseable storage as absent", () => {
    expect(read(store({ "kubeside.prefs": "{{{" }))).toEqual(DEFAULT);
  });

  it("treats a theme it does not know as absent", () => {
    const got = read(store({ "kubeside.prefs": '{"theme":"solarized","scale":1.1}' }));
    expect(got.theme).toBe("system");
    // The half it could read still counts.
    expect(got.scale).toBe(1.1);
  });

  it("clamps a scale from outside the ladder", () => {
    expect(read(store({ "kubeside.prefs": '{"scale":9}' })).scale).toBe(1.6);
    expect(read(store({ "kubeside.prefs": '{"scale":0.1}' })).scale).toBe(0.9);
    expect(read(store({ "kubeside.prefs": '{"scale":"big"}' })).scale).toBe(1);
  });

  // Private browsing throws on access rather than returning null.
  it("survives storage that throws", () => {
    const hostile = {
      getItem() { throw new Error("denied"); },
      setItem() { throw new Error("denied"); },
      removeItem() { throw new Error("denied"); },
    };
    expect(read(hostile)).toEqual(DEFAULT);
    expect(() => write(hostile, { theme: "dark", scale: 1.1 })).not.toThrow();
  });

  it("survives having no storage at all", () => {
    expect(read(null)).toEqual(DEFAULT);
    expect(() => write(null, DEFAULT)).not.toThrow();
  });
});

describe("resolveTheme", () => {
  // "system" is not a third look. It is a deferral, and it keeps deferring.
  it("asks the system only while nothing is pinned", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
    expect(resolveTheme("light", true)).toBe("light");
  });
});

describe("cycleTheme", () => {
  it("returns to following the system", () => {
    expect(cycleTheme("system")).toBe("light");
    expect(cycleTheme("light")).toBe("dark");
    expect(cycleTheme("dark")).toBe("system");
  });
});

describe("step", () => {
  it("walks the ladder", () => {
    expect(step(1, 1)).toBe(1.1);
    expect(step(1, -1)).toBe(0.9);
    expect(step(1.25, 1)).toBe(1.4);
  });

  // A key held down must stop at the end rather than wrap to the other one.
  it("stops at both ends", () => {
    expect(step(1.6, 1)).toBe(1.6);
    expect(step(0.9, -1)).toBe(0.9);
  });

  // Storage written by an older build, or by hand, can hold a value between
  // rungs. Stepping snaps to the ladder instead of drifting off it forever.
  it("snaps a value that is not on the ladder", () => {
    expect(step(1.13, 1)).toBe(1.25);
    expect(step(1.13, -1)).toBe(1);
  });

  it("has six rungs, ending where the design says", () => {
    expect(SCALES).toEqual([0.9, 1, 1.1, 1.25, 1.4, 1.6]);
  });
});

describe("apply", () => {
  it("sets the resolved theme and the scale", () => {
    const r = root();
    apply(r, { theme: "system", scale: 1.25 }, false);
    expect(r.attrs["data-theme"]).toBe("light");
    expect(r.vars["--scale"]).toBe("1.25");
  });

  it("writes the pinned theme rather than the system one", () => {
    const r = root();
    apply(r, { theme: "dark", scale: 1 }, false);
    expect(r.attrs["data-theme"]).toBe("dark");
  });
});

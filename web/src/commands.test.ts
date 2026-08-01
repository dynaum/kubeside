import { describe, expect, it } from "vitest";
import { commands, groups, move, search, split } from "./commands";
import type { AppView, ContextView } from "./api";
import type { Route } from "./route";

function ctx(name: string, environment: string): ContextView {
  return {
    name, environment, current: false, state: "live", hasData: true,
    risk: "low", color: "green", hazard: false, write: "allow",
  };
}

function app(namespace: string, name: string, health = "healthy"): AppView {
  return {
    namespace, name, health, kind: "Deployment", reason: "", detail: "",
    groupedBy: "label", ready: "2/2", objects: 3, pods: 2, restarts: 0, measured: 0,
  };
}

const appsRoute: Route = { screen: "apps", context: "prod" };
const appRoute: Route = { screen: "app", context: "prod", namespace: "team-a", workload: "payments" };

describe("commands", () => {
  it("offers every app in the current environment", () => {
    const list = commands([ctx("prod", "prod")], ctx("prod", "prod"), [app("team-a", "payments")], appsRoute);
    expect(list.some((c) => c.label.includes("payments"))).toBe(true);
  });

  // Offering "tail logs" with nothing selected would be a command that cannot
  // run, which is worse than one that is absent.
  it("offers app actions only when a screen is about an app", () => {
    const none = commands([], ctx("prod", "prod"), [], appsRoute);
    expect(none.some((c) => c.group === "Actions")).toBe(false);

    const some = commands([], ctx("prod", "prod"), [], appRoute);
    expect(some.filter((c) => c.group === "Actions").length).toBeGreaterThan(2);
  });

  it("offers the other environments as switches", () => {
    const list = commands([ctx("prod", "prod"), ctx("stg", "stg")], ctx("prod", "prod"), [], appsRoute);
    const envs = list.filter((c) => c.group === "Environments");
    expect(envs).toHaveLength(1);
    expect(envs[0].label).toContain("stg");
  });

  it("does not offer switching to where you already are", () => {
    const list = commands([ctx("prod", "prod")], ctx("prod", "prod"), [], appsRoute);
    expect(list.some((c) => c.group === "Environments")).toBe(false);
  });
});

describe("search", () => {
  const list = commands(
    [ctx("prod", "prod"), ctx("stg", "stg")],
    ctx("prod", "prod"),
    [app("team-a", "payments"), app("team-a", "checkout"), app("team-b", "search-indexer", "failed")],
    appRoute,
  );

  it("returns everything for an empty query, so the palette is also a menu", () => {
    expect(search(list, "").length).toBe(list.length);
  });

  it("finds a contiguous match", () => {
    const hits = search(list, "pay");
    expect(hits.length).toBeGreaterThan(0);
    expect(hits[0].command.label).toContain("payments");
  });

  // "tacheck" finding team-a / checkout is the behaviour every palette a
  // developer has used already has.
  it("matches a subsequence across words", () => {
    const hits = search(list, "tacheck");
    expect(hits.some((h) => h.command.label.includes("checkout"))).toBe(true);
  });

  it("ranks a contiguous match above a scattered one", () => {
    const hits = search(list, "search");
    expect(hits[0].command.label).toContain("search-indexer");
  });

  it("returns nothing when a character is simply absent", () => {
    expect(search(list, "zzzz")).toHaveLength(0);
  });

  it("is case-insensitive", () => {
    expect(search(list, "PAYMENTS").length).toBeGreaterThan(0);
  });

  it("reports the ranges it matched, so they can be highlighted", () => {
    const hit = search(list, "pay")[0];
    expect(hit.ranges.length).toBeGreaterThan(0);
    const [from, to] = hit.ranges[0];
    expect(hit.command.label.slice(from, to).toLowerCase()).toBe("pay");
  });
});

describe("groups", () => {
  it("keeps groups in the order they first appear", () => {
    const list = commands(
      [ctx("prod", "prod"), ctx("stg", "stg")],
      ctx("prod", "prod"),
      [app("team-a", "payments")],
      appRoute,
    );
    const names = groups(search(list, "")).map((g) => g.name);
    expect(names[0]).toBe("Apps");
    expect(names).toContain("Actions");
    expect(names).toContain("Environments");
  });
});

describe("move", () => {
  // A list that stops at the end makes you reverse direction to reach the item
  // just past it.
  it("wraps at both ends", () => {
    expect(move(2, 1, 3)).toBe(0);
    expect(move(0, -1, 3)).toBe(2);
  });

  it("survives an empty list", () => {
    expect(move(0, 1, 0)).toBe(0);
  });
});

describe("split", () => {
  it("splits a label around its matched ranges", () => {
    expect(split("payments", [[0, 3]])).toEqual([
      { text: "pay", hit: true },
      { text: "ments", hit: false },
    ]);
  });

  it("returns one plain part when nothing matched", () => {
    expect(split("payments", [])).toEqual([{ text: "payments", hit: false }]);
  });
});

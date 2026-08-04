import { describe, expect, it } from "vitest";
import { envToken, loudestEnv } from "./health";
import type { FleetRow } from "./api";

describe("envToken", () => {
  it("maps the colours the backend names", () => {
    expect(envToken({ color: "green", risk: "low" })).toBe("qa");
    expect(envToken({ color: "amber", risk: "medium" })).toBe("stg");
    expect(envToken({ color: "red", risk: "high" })).toBe("prod");
    expect(envToken({ color: "violet", risk: "high" })).toBe("unc");
  });

  // A config may name a colour this UI has no token for. Risk still decides,
  // so the edge never goes blank and never goes quiet on a dangerous cluster.
  it("falls back to risk for a colour it does not know", () => {
    expect(envToken({ color: "chartreuse", risk: "low" })).toBe("qa");
    expect(envToken({ color: "chartreuse", risk: "medium" })).toBe("stg");
    expect(envToken({ color: "chartreuse", risk: "high" })).toBe("prod");
  });

  // Never render an unknown as safe.
  it("treats missing information as unclassified", () => {
    expect(envToken({})).toBe("unc");
    expect(envToken({ risk: "" })).toBe("unc");
  });
});

// A fleet row names its environment and carries the backend's verdict on it
// under its own fields. The name alone cannot be classified here: Classify
// matches by keyword token and returns the name untouched, so prod-us-east is
// red while spelling no tier a screen could compare against. Reading colour and
// risk keeps one rule in one language.
describe("envToken on a fleet row", () => {
  const row = (env: string, envColor: string, envRisk: string): FleetRow =>
    ({ context: env, clusterId: "https://" + env, env, envColor, envRisk, state: "present" });

  it("colours a cluster the backend classified, whatever the cluster is called", () => {
    const cases: [FleetRow, string][] = [
      [row("prod-us-east", "red", "high"), "prod"],
      [row("production", "red", "high"), "prod"],
      [row("staging-eks", "amber", "medium"), "stg"],
      [row("uat", "amber", "medium"), "stg"],
      [row("sandbox-3", "green", "low"), "qa"],
      [row("dev-cluster", "green", "low"), "qa"],
      [row("dr-frankfurt", "violet", "high"), "unc"],
    ];
    for (const [r, want] of cases) {
      expect(envToken({ color: r.envColor, risk: r.envRisk })).toBe(want);
    }
  });

  // A row from an older server, or one whose environment nobody resolved,
  // still lands on unclassified rather than on a tier it did not earn.
  it("falls back to unclassified when a row carries no classification", () => {
    const bare: FleetRow = { context: "edge", clusterId: "", env: "edge", state: "absent" };
    expect(envToken({ color: bare.envColor, risk: bare.envRisk })).toBe("unc");
  });
});

// One banner speaks for several clusters, so it takes the edge of the loudest
// environment it names rather than inheriting the shell's.
describe("loudestEnv", () => {
  it("takes the loudest environment in the set", () => {
    expect(loudestEnv([{ color: "green" }, { color: "red" }, { color: "amber" }])).toBe("prod");
    expect(loudestEnv([{ color: "green" }, { color: "amber" }])).toBe("stg");
    expect(loudestEnv([{ color: "green" }])).toBe("qa");
  });

  // Unclassified carries prod-strength styling, so it outranks the tiers below
  // it. A named prod cluster still wins: red is the edge that reads.
  it("ranks unclassified above the tiers it outranks and below prod", () => {
    expect(loudestEnv([{ color: "violet" }, { color: "green" }])).toBe("unc");
    expect(loudestEnv([{ color: "violet" }, { color: "red" }])).toBe("prod");
  });

  it("treats an empty set as unclassified", () => {
    expect(loudestEnv([])).toBe("unc");
  });
});

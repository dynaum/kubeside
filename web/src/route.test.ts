import { describe, expect, it } from "vitest";
import { parseRoute, routeHash } from "./route";

describe("parseRoute", () => {
  it("reads a logs permalink", () => {
    expect(parseRoute("#logs/prod-eks/team-a/checkout")).toEqual({
      screen: "logs", context: "prod-eks", namespace: "team-a", workload: "checkout",
    });
  });

  it("reads an app detail permalink", () => {
    expect(parseRoute("#app/prod-eks/team-a/checkout")).toEqual({
      screen: "app", context: "prod-eks", namespace: "team-a", workload: "checkout",
    });
  });

  it("reads an apps route", () => {
    expect(parseRoute("#apps/qa1")).toEqual({ screen: "apps", context: "qa1" });
  });

  // Anything unrecognized lands on the app list rather than a blank screen.
  it("falls back to the app list", () => {
    expect(parseRoute("")).toEqual({ screen: "apps" });
    expect(parseRoute("#nonsense")).toEqual({ screen: "apps" });
    expect(parseRoute("#logs/only-a-context")).toEqual({ screen: "apps" });
  });

  it("decodes names that need escaping", () => {
    const hash = routeHash({ screen: "logs", context: "arn:aws:eks:us-east-1/x", namespace: "team-a", workload: "check/out" });
    expect(parseRoute(hash)).toEqual({
      screen: "logs", context: "arn:aws:eks:us-east-1/x", namespace: "team-a", workload: "check/out",
    });
  });

  it("round-trips the fleet route", () => {
    const r = { screen: "fleet", app: "checkout", namespace: "shop" } as const;
    expect(parseRoute(routeHash(r))).toEqual(r);
  });

  it("falls back to apps when the fleet route is missing its app", () => {
    expect(parseRoute("#fleet")).toEqual({ screen: "apps" });
  });
});

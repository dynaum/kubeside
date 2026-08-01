import { describe, expect, it } from "vitest";
import { applyPatch } from "./stream";
import type { AppView, AppsView } from "./api";

function app(namespace: string, name: string, health: string): AppView {
  return {
    namespace, name, health,
    kind: "Deployment", reason: "", detail: "", groupedBy: "label", ready: "1/1", objects: 1, pods: 1, restarts: 0,
  };
}

function base(...apps: AppView[]): AppsView {
  return {
    context: "qa", state: "live", scope: "cluster-wide", apps,
    metrics: { source: "metrics-server", available: true },
  };
}

describe("applyPatch", () => {
  it("adds a new app", () => {
    const next = applyPatch(base(app("team-a", "checkout", "healthy")), {
      added: [app("team-b", "search", "healthy")],
    });
    expect(next.apps.map((a) => a.name)).toEqual(["checkout", "search"]);
  });

  it("replaces a changed app in place", () => {
    const next = applyPatch(
      base(app("team-a", "checkout", "healthy"), app("team-b", "search", "healthy")),
      { changed: [app("team-a", "checkout", "failed")] },
    );
    expect(next.apps[0].health).toBe("failed");
    expect(next.apps).toHaveLength(2);
  });

  it("removes a deleted app by key", () => {
    const next = applyPatch(
      base(app("team-a", "checkout", "healthy"), app("team-b", "search", "healthy")),
      { removed: ["team-b/search"] },
    );
    expect(next.apps.map((a) => a.name)).toEqual(["checkout"]);
  });

  // A cluster going stale must reach the screen, or the UI keeps claiming data
  // is live when the backend has said otherwise.
  it("applies view metadata", () => {
    const next = applyPatch(base(app("team-a", "checkout", "healthy")), {
      meta: {
        state: "stale", scope: "cluster-wide", partial: ["CronJob"],
        metrics: { source: "none", available: false },
      },
    });
    expect(next.state).toBe("stale");
    expect(next.partial).toEqual(["CronJob"]);
    expect(next.metrics.available).toBe(false);
  });

  it("leaves everything untouched for an empty patch", () => {
    const before = base(app("team-a", "checkout", "healthy"));
    expect(applyPatch(before, {})).toEqual(before);
  });

  // Absent is not empty. A patch that omits a field must not blank the field.
  it("does not clear metadata the patch omitted", () => {
    const before = { ...base(app("team-a", "checkout", "healthy")), partial: ["Ingress"] };
    const next = applyPatch(before, { changed: [app("team-a", "checkout", "failed")] });
    expect(next.partial).toEqual(["Ingress"]);
    expect(next.scope).toBe("cluster-wide");
  });

  it("does not mutate the previous view", () => {
    const before = base(app("team-a", "checkout", "healthy"));
    applyPatch(before, { changed: [app("team-a", "checkout", "failed")] });
    expect(before.apps[0].health).toBe("healthy");
  });
});

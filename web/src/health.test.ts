import { describe, expect, it } from "vitest";
import { envToken } from "./health";

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

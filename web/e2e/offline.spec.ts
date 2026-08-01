import { expect, test } from "@playwright/test";

// kubeside is one binary a developer runs against clusters, often from a
// VPN-only laptop or a host with egress rules. Anything the UI fetches from
// somebody else's origin is a thing that fails there, silently, and changes the
// product without saying so.
//
// This is a gate rather than a note, because the failure is invisible on a
// developer machine with open egress and invisible in CI for the same reason.
// Only an assertion catches it.

const token = "?t=fixture-token";

test("the UI loads nothing from another origin", async ({ page, baseURL }) => {
  const origin = new URL(baseURL ?? "http://127.0.0.1").origin;
  const foreign: string[] = [];

  page.on("request", (req) => {
    const url = req.url();
    if (url.startsWith("data:") || url.startsWith("blob:")) return;
    if (!url.startsWith(origin)) foreign.push(`${req.resourceType()} ${url}`);
  });

  // Every screen, because a font or an icon can be pulled in by one stylesheet
  // that only one route loads.
  for (const hash of ["#apps/qa1", "#app/qa1/team-a/payments", "#config/qa1/team-a/payments", "#promotion"]) {
    await page.goto(`/${token}${hash}`);
    await page.waitForLoadState("networkidle");
  }

  expect(foreign, "the UI must be self-contained; these left the origin").toEqual([]);
});

// The fonts are the design system, and the design system is the differentiator.
// Losing them to a blocked request is not a cosmetic failure.
test("the typeface is served from the binary", async ({ page }) => {
  const fonts: string[] = [];
  page.on("response", (res) => {
    if (res.url().includes(".woff")) fonts.push(res.url());
  });

  await page.goto(`/${token}#apps/qa1`);
  await page.waitForLoadState("networkidle");

  expect(fonts.length, "no webfont was served at all").toBeGreaterThan(0);
  for (const url of fonts) {
    expect(url).toContain("ibm-plex");
  }
});

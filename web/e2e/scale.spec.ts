import { expect, test } from "@playwright/test";

// The type scale, asserted as numbers. The pixel gate cannot do this job: it
// tolerates 2592 changed pixels, which is enough to hide a whole column, and it
// would not tell you whether a row grew or the text simply clipped inside it.

const token = "?t=fixture-token";

// The healthy row on purpose. Its "why" cell is empty, and that cell wraps, so
// a row with an explanation in it legitimately grows taller than --row-h once
// the text is large enough to take two lines. Measuring a row that cannot wrap
// isolates the row height from the wrapping.
const rowHeight = (page: import("@playwright/test").Page) =>
  page.locator("table.tbl tbody tr", { hasText: "checkout" })
    .evaluate((el) => el.getBoundingClientRect().height);

// getPropertyValue hands back the unresolved calc(), so the token is measured
// on a throwaway element that actually uses it.
const rowToken = (page: import("@playwright/test").Page) =>
  page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.cssText = "position:absolute;visibility:hidden;height:var(--row-h)";
    document.body.appendChild(probe);
    const h = parseFloat(getComputedStyle(probe).height);
    probe.remove();
    return h;
  });

const fontSize = (page: import("@playwright/test").Page, sel: string) =>
  page.locator(sel).first().evaluate((el) => parseFloat(getComputedStyle(el).fontSize));

test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date("2026-07-25T03:00:00Z"));
  await page.goto(`/${token}#apps/qa1`);
  await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);
});

test("grows the row with the text, so nothing clips", async ({ page }) => {
  const before = { row: await rowHeight(page), cell: await fontSize(page, "table.tbl tbody td") };
  expect(before.row).toBeCloseTo(34, 0);
  expect(await rowToken(page)).toBeCloseTo(34, 0);

  await page.keyboard.press("ControlOrMeta+Alt+Equal");

  const after = { row: await rowHeight(page), cell: await fontSize(page, "table.tbl tbody td") };
  expect(after.cell).toBeGreaterThan(before.cell);
  // 1.1 is the first rung.
  expect(after.row).toBeCloseTo(before.row * 1.1, 0);
});

// The whole reason this is not browser zoom. Zoom takes the grid with it and
// costs rows; this leaves the grid where it is.
test("leaves the spacing grid alone", async ({ page }) => {
  const gap = () => page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue("--s4").trim());

  const before = await gap();
  await page.keyboard.press("ControlOrMeta+Alt+Equal");
  await page.keyboard.press("ControlOrMeta+Alt+Equal");
  expect(await gap()).toBe(before);
});

test("steps both ways and resets", async ({ page }) => {
  const base = await rowHeight(page);

  await page.keyboard.press("ControlOrMeta+Alt+Equal");
  expect(await rowHeight(page)).toBeGreaterThan(base);

  await page.keyboard.press("ControlOrMeta+Alt+Minus");
  expect(await rowHeight(page)).toBeCloseTo(base, 0);

  await page.keyboard.press("ControlOrMeta+Alt+Equal");
  await page.keyboard.press("ControlOrMeta+Alt+Digit0");
  expect(await rowHeight(page)).toBeCloseTo(base, 0);
});

// A held key must settle at the end of the ladder rather than wrap around to
// the other extreme, which would be a very unpleasant surprise.
test("stops at the ends of the ladder", async ({ page }) => {
  for (let i = 0; i < 10; i++) await page.keyboard.press("ControlOrMeta+Alt+Equal");
  expect(await rowToken(page)).toBeCloseTo(34 * 1.6, 0);
  expect(await rowHeight(page)).toBeCloseTo(34 * 1.6, 0);

  for (let i = 0; i < 10; i++) await page.keyboard.press("ControlOrMeta+Alt+Minus");
  expect(await rowToken(page)).toBeCloseTo(34 * 0.9, 0);
});

test("survives a reload", async ({ page }) => {
  await page.keyboard.press("ControlOrMeta+Alt+Equal");
  const grown = await rowHeight(page);

  await page.reload();
  await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);
  expect(await rowHeight(page)).toBeCloseTo(grown, 0);
});

// The rail holds environment names and the log gutter holds pod names. Text
// that grew past a width that did not would be text nobody can read.
test("grows the widths that hold text", async ({ page }) => {
  const rail = () => page.locator(".rail").evaluate((el) => el.getBoundingClientRect().width);

  const before = await rail();
  await page.keyboard.press("ControlOrMeta+Alt+Equal");
  expect(await rail()).toBeGreaterThan(before);
});

test("is reachable from the palette", async ({ page }) => {
  const base = await rowHeight(page);

  await page.keyboard.press("ControlOrMeta+k");
  await page.waitForSelector(".palette");
  await page.keyboard.type("larger");
  await page.keyboard.press("Enter");

  await expect(page.locator(".palette")).toBeHidden();
  expect(await rowHeight(page)).toBeGreaterThan(base);
});

// A dense table at 1.6 is wider than the window. That is the trade the top of
// the ladder makes, and it is fine as long as the table scrolls rather than
// cutting a column off where nobody can reach it.
test("keeps every column reachable at the largest size", async ({ page }) => {
  for (let i = 0; i < 10; i++) await page.keyboard.press("ControlOrMeta+Alt+Equal");

  const geometry = await page.evaluate(() => {
    const region = document.querySelector(".page")!;
    return {
      scrollable: region.scrollWidth > region.clientWidth,
      // The window itself must not scroll sideways; the table region owns it.
      bodyOverflows: document.body.scrollWidth > document.body.clientWidth,
    };
  });

  expect(geometry.scrollable).toBe(true);
  expect(geometry.bodyOverflows).toBe(false);

  await page.locator("table.tbl tbody tr", { hasText: "checkout" })
    .locator(".btn").scrollIntoViewIfNeeded();
  await expect(page.locator("table.tbl tbody tr", { hasText: "checkout" }).locator(".btn"))
    .toBeInViewport();
});

import { expect, test } from "@playwright/test";

// The light theme shipped complete in tokens.css and unreachable, because
// index.html pinned data-theme="dark". These tests exist so it cannot go
// unreachable again without something going red.
//
// The screenshot gate cannot cover this on its own: it pins colorScheme dark
// for every other test, so light had never rendered in CI at all.

const token = "?t=fixture-token";

test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date("2026-07-25T03:00:00Z"));
});

test.describe("following the system", () => {
  test.use({ colorScheme: "light" });

  // The CSP forbids inline scripts, so nothing can set the theme before paint.
  // CSS has to answer, or a developer on a light desktop watches the product
  // flash dark and correct itself on every launch.
  test("paints light on a light desktop, with no script involved", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr").first()).toBeVisible();

    const bg = await page.evaluate(() =>
      getComputedStyle(document.body).backgroundColor);
    // #F4F2ED, the warm paper the design system specifies. Not white.
    expect(bg).toBe("rgb(244, 242, 237)");
  });

  test("keeps following when the desktop changes its mind", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr").first()).toBeVisible();

    await page.emulateMedia({ colorScheme: "dark" });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

    await page.emulateMedia({ colorScheme: "light" });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  });
});

test.describe("pinning a theme", () => {
  test.use({ colorScheme: "dark" });

  test("survives a reload and stops following the system", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr").first()).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

    // system -> light
    await page.keyboard.press("ControlOrMeta+Shift+KeyL");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

    await page.reload();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");

    // Pinned means pinned: the desktop no longer gets a say.
    await page.emulateMedia({ colorScheme: "dark" });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  });

  test("cycles back to following the system", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr").first()).toBeVisible();

    await page.keyboard.press("ControlOrMeta+Shift+KeyL"); // light
    await page.keyboard.press("ControlOrMeta+Shift+KeyL"); // dark
    await page.keyboard.press("ControlOrMeta+Shift+KeyL"); // system
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

    await page.emulateMedia({ colorScheme: "light" });
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  });

  test("is reachable from the palette, not only from a shortcut", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr").first()).toBeVisible();

    await page.keyboard.press("ControlOrMeta+k");
    await page.waitForSelector(".palette");

    // Discoverable by opening the palette, not only by knowing the shortcut.
    // The palette is the product's answer to "where is the setting", so the
    // group has to be there for somebody who arrives without being told.
    await expect(page.locator(".palette-group", { hasText: "Settings" })).toBeVisible();
    await expect(page.locator(".palette-row", { hasText: "Text size" }).first()).toBeVisible();

    await page.keyboard.type("theme");
    await page.keyboard.press("Enter");

    await expect(page.locator(".palette")).toBeHidden();
    await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  });
});

test.describe("the light apps list", () => {
  test.use({ colorScheme: "light" });

  test("renders", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);
    await expect(page).toHaveScreenshot("apps-light.png", { fullPage: true });
  });
});

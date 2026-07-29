import { expect, test } from "@playwright/test";

// Focus and semantics are only true in a browser, so this is where they are
// checked. Nothing here compares pixels: the visual gate owns that, and these
// two files fail for different reasons on purpose.
//
// The surfaces covered are the two that matter most. The palette is how a
// keyboard user reaches everything, and the confirmation dialog is what stands
// between somebody and an action in production.

const token = "?t=fixture-token";

test.beforeEach(async ({ page }) => {
  await page.clock.setFixedTime(new Date("2026-07-25T03:00:00Z"));
});

async function openPalette(page: import("@playwright/test").Page) {
  await page.goto(`/${token}#apps/qa1`);
  await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();
}

test.describe("the command palette", () => {
  // A dialog with no accessible name is announced as "dialog" and nothing else,
  // which tells somebody they are trapped without saying in what. openPalette
  // asserts the name; this asserts the rest of the page goes away, which is
  // what modal means to a screen reader.
  test("hides the page behind it", async ({ page }) => {
    await openPalette(page);
    const hidden = await page.evaluate(() => {
      const root = document.getElementById("root");
      return root?.getAttribute("aria-hidden") === "true" || root?.hasAttribute("inert");
    });
    expect(hidden).toBe(true);
  });

  // Arrowing through a list announces nothing unless the options are options
  // and the input says which one is active.
  test("announces which command is active", async ({ page }) => {
    await openPalette(page);
    const input = page.getByRole("combobox");
    await expect(input).toBeFocused();

    const first = await input.getAttribute("aria-activedescendant");
    expect(first).toBeTruthy();
    await expect(page.locator(`#${first}`)).toHaveAttribute("aria-selected", "true");

    await page.keyboard.press("ArrowDown");
    const second = await input.getAttribute("aria-activedescendant");
    expect(second).not.toBe(first);
    await expect(page.locator(`#${second}`)).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("listbox")).toBeVisible();
  });

  // Tab has to stay inside. Leaving the palette open while focus wanders into
  // the app behind it is the failure a sighted mouse user never sees.
  test("keeps focus inside while it is open", async ({ page }) => {
    await openPalette(page);
    for (let i = 0; i < 6; i++) await page.keyboard.press("Tab");
    const inside = await page.evaluate(() => {
      const dialog = document.querySelector('[role="dialog"]');
      return !!dialog && !!document.activeElement && dialog.contains(document.activeElement);
    });
    expect(inside).toBe(true);
  });

  // Focus has to come back to whatever the developer was on. Dumped on <body>,
  // the next Tab restarts from the top of the page rather than where they were.
  test("closes on Escape and gives focus back to where it came from", async ({ page }) => {
    await page.goto(`/${token}#apps/qa1`);
    await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);

    const filter = page.getByPlaceholder("filter");
    await filter.focus();
    await page.keyboard.press("ControlOrMeta+k");
    await expect(page.getByRole("dialog", { name: "Command palette" })).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(filter).toBeFocused();
  });
});

test.describe("the confirmation dialog", () => {
  test("is a named dialog that traps focus and closes on Escape", async ({ page }) => {
    await page.goto(`/${token}#app/prod-us-east/team-a/payments`);
    await page.getByRole("button", { name: /unlock writes/i }).click();

    const dialog = page.getByRole("dialog", { name: /unlock|prod/i });
    await expect(dialog).toBeVisible();

    for (let i = 0; i < 6; i++) await page.keyboard.press("Tab");
    const inside = await page.evaluate(() => {
      const d = document.querySelector('[role="dialog"]');
      return !!d && !!document.activeElement && d.contains(document.activeElement);
    });
    expect(inside).toBe(true);

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });
});

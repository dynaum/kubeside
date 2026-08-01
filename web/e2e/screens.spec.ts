import { expect, test } from "@playwright/test";

// One test per screen: it renders what it should say, and it looks the way it
// looked last time. The first assertion catches a broken screen, the second
// catches a design regression nobody meant to ship.

const token = "?t=fixture-token";

test.beforeEach(async ({ page }) => {
  // The clock is in the UI: revision ages, "now" on the timeline axis. Freezing
  // it is what keeps a screenshot from failing tomorrow for no reason.
  await page.clock.setFixedTime(new Date("2026-07-25T03:00:00Z"));
});

test("apps list", async ({ page }) => {
  await page.goto(`/${token}#apps/qa1`);
  await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);
  // Worst first: the failing app leads the table.
  await expect(page.locator("table.tbl tbody tr").first()).toContainText("search-indexer");
  await expect(page.locator("table.tbl tbody tr").last()).toContainText("checkout");
  // The row answers what is running, since when, and whether it is flapping.
  // Asserted as text because the pixel gate's antialiasing tolerance is wide
  // enough to let a whole column swap through unnoticed.
  const payments = page.locator("table.tbl tbody tr", { hasText: "payments" });
  await expect(payments).toContainText("3.2.1");
  await expect(payments).toContainText("1h");
  await expect(payments).toContainText("14");
  // Never scheduled, so no pod was read and no restart count was measured.
  await expect(page.locator("table.tbl tbody tr", { hasText: "billing" })).toContainText("—");
  await expect(page).toHaveScreenshot("apps.png", { fullPage: true });
});

test("app detail and timeline", async ({ page }) => {
  await page.goto(`/${token}#app/qa1/team-a/payments`);
  await expect(page.locator(".stat-value").first()).toHaveText("5/6");
  // Horizon honesty: every lane says what it cannot know, and an unreadable
  // source says what it needs.
  await expect(page.locator(".tl-horizon .cap", { hasText: "reconstruction ends" })).toBeVisible();
  await expect(page.locator(".tl-lane", { hasText: "Helm" })).toContainText("needs read access to secrets");
  // A kubectl actor is the out-of-band change worth surfacing, and it sits on
  // the rollout it caused rather than at the top of the table.
  await expect(page.locator("table.tbl tbody tr", { hasText: "revision 12" })).toContainText("kubectl");
  await expect(page).toHaveScreenshot("app-detail.png", { fullPage: true });
});

test("resolved configuration", async ({ page }) => {
  await page.goto(`/${token}#config/qa1/team-a/payments`);
  await expect(page.getByText("MAX_CONNECTIONS")).toBeVisible();
  // A secret is masked and its reveal is disabled with the verb named.
  await expect(page.getByRole("button", { name: "Reveal" })).toBeDisabled();
  await expect(page.locator(".tag-unknown").first()).toContainText("not recoverable");
  await expect(page).toHaveScreenshot("config.png", { fullPage: true });
});

test("promotion matrix", async ({ page }) => {
  await page.goto(`/${token}#promotion`);
  await expect(page.getByRole("heading", { name: "Promotion" })).toBeVisible();
  // Ahead of upstream floats to the top, because it is the worst thing here.
  await expect(page.locator("table.matrix tbody tr").first()).toContainText("search-indexer");
  await expect(page.locator(".drift-flag.ahead")).toHaveText("ahead of upstream");
  await expect(page.locator(".cell-none")).toHaveCount(1);
  await expect(page).toHaveScreenshot("promotion.png", { fullPage: true });
});

test("command palette", async ({ page }) => {
  await page.goto(`/${token}#apps/qa1`);
  await expect(page.locator("table.tbl tbody tr")).toHaveCount(4);
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByPlaceholder("jump to an app, or run something")).toBeVisible();
  await page.keyboard.type("pay");
  await expect(page.locator(".palette-row").first()).toContainText("payments");
  await expect(page).toHaveScreenshot("palette.png");
});

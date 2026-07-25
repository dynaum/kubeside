import { defineConfig, devices } from "@playwright/test";

// Visual regression is a product risk when design is the differentiator, so the
// gate is deliberately strict: one fixed viewport, animations disabled, and a
// hard fail on any pixel that moves without somebody meaning it.
//
// Baselines are generated in the same Linux container CI uses. Font rendering
// differs enough between platforms that a macOS baseline would fail every CI
// run and train everybody to pass --update-snapshots without looking.
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? "line" : "list",

  expect: {
    toHaveScreenshot: {
      // A handful of antialiased pixels is not a design regression; a moved
      // control is. This threshold separates them.
      maxDiffPixelRatio: 0.002,
      animations: "disabled",
    },
  },

  use: {
    baseURL: `http://127.0.0.1:${process.env.PORT ?? 4321}`,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    colorScheme: "dark",
    ...devices["Desktop Chrome"],
  },

  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } }],

  webServer: {
    command: "node e2e/server.mjs",
    url: `http://127.0.0.1:${process.env.PORT ?? 4321}/index.html`,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});

import { defineConfig, devices } from "@playwright/test";

/**
 * The play screen's measurements (e2e/play-measure.perf.ts), against a stack
 * that is already up, with the smoke test's variables and seed
 * (playwright.config.ts). Run on demand by `make e2e-measure`; it is a
 * separate config so that `npx playwright test` and CI never pick it up.
 *
 *   E2E_MEASURE_OUT    the JSON file the numbers are written to
 *                      (test-results/play-measurements.json by default)
 *   E2E_MEASURE_LOAD_WAIT_MINUTES
 *                      how long to wait for the machine's one-minute load
 *                      to fall to 0.6 per core before a run (30 by default)
 *   E2E_MEASURE_TRACE  a directory: record a Chromium trace of each
 *                      scenario's first run into it, for finding what a slow
 *                      one spent its time on
 */
if (process.env.E2E_ENV_FILE) process.loadEnvFile(process.env.E2E_ENV_FILE);

export default defineConfig({
  testDir: "./e2e",
  testMatch: "*.perf.ts",
  globalSetup: "./e2e/seed.ts",
  globalTeardown: "./e2e/teardown.ts",
  workers: 1,
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  // Each test runs six times and may wait out a busy machine before each.
  timeout: 6 * 60 * 60_000,
  expect: { timeout: 30_000 },
  reporter: [["list"]],
  outputDir: "./test-results",
  use: {
    baseURL: process.env.E2E_BASE_URL,
    ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === "1",
    locale: "en-US",
    actionTimeout: 60_000,
    navigationTimeout: 60_000,
    trace: "off",
    screenshot: "off",
  },
  projects: [
    {
      name: "chromium",
      // The "chromium" channel is the full browser in new headless mode; the
      // default headless shell is a different build, and its rendering path
      // is not the one a participant's Chrome uses.
      use: { ...devices["Desktop Chrome"], channel: "chromium", viewport: { width: 1920, height: 1080 } },
    },
  ],
});

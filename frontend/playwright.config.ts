import { defineConfig, devices } from "@playwright/test";

/**
 * The browser smoke test of the main path (e2e/), run against a stack that is
 * already up: `make e2e-smoke` from the repository root, or
 * `npx playwright test` here with the variables below set. It starts no
 * server of its own, because what it proves is that a deployed stack works.
 *
 *   E2E_BASE_URL             the stack's public address (required)
 *   E2E_ADMIN_LOGIN          an administrator to seed the run with ("admin")
 *   E2E_ADMIN_PASSWORD       its password, or its one-time password
 *   E2E_IGNORE_HTTPS_ERRORS  "1" for a certificate from a local CA
 *   E2E_ENV_FILE             a file of the above, such as the smoke.env
 *                            e2e/stack.sh writes; the environment wins
 */
if (process.env.E2E_ENV_FILE) process.loadEnvFile(process.env.E2E_ENV_FILE);

export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./e2e/seed.ts",
  globalTeardown: "./e2e/teardown.ts",
  // One participant, one contest, one database: the specs share them, so
  // they run one at a time and in file order.
  workers: 1,
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  outputDir: "./test-results",
  use: {
    baseURL: process.env.E2E_BASE_URL,
    ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === "1",
    // The assertions read English text, so the interface must be in English
    // whatever the machine's own language.
    locale: "en-US",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});

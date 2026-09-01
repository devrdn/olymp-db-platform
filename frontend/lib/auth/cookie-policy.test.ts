import { afterEach, describe, expect, test } from "vitest";

import { cookieSecure } from "./cookie-policy";

const original = { ...process.env };

// NODE_ENV is typed read-only, which is right for application code and in the
// way here: these tests exist to pin what happens when the build kind and the
// deployment disagree, so both have to be set.
const setEnv = (key: string, value?: string) => {
  const env = process.env as Record<string, string | undefined>;
  if (value === undefined) delete env[key];
  else env[key] = value;
};

afterEach(() => {
  process.env = { ...original };
});

/**
 * The bug this file exists for.
 *
 * `next start` sets NODE_ENV=production, so a production build served over
 * plain HTTP — which is exactly what `make front-start` does locally — marked
 * the session cookie Secure. A browser silently discards a Secure cookie
 * delivered over http, so signing in appeared to succeed and the next click
 * went back to the form. The project's own Caddyfile documents that failure
 * mode; the interface walked into it anyway.
 *
 * NODE_ENV describes how the code was built. Whether the deployment is served
 * over TLS is a different fact, and it is the one that decides this — the same
 * reasoning the Go CookieWriter already carries.
 */
describe("cookieSecure", () => {
  test("obeys the deployment when it says so", () => {
    setEnv("COOKIE_SECURE", "false");
    setEnv("NODE_ENV", "production");

    expect(cookieSecure()).toBe(false);
  });

  test("marks the cookie Secure where the deployment says it is served over TLS", () => {
    setEnv("COOKIE_SECURE", "true");
    setEnv("NODE_ENV", "development");

    expect(cookieSecure()).toBe(true);
  });

  test("defaults to secure for a production build, so forgetting it fails safe", () => {
    setEnv("COOKIE_SECURE");
    setEnv("NODE_ENV", "production");

    expect(cookieSecure()).toBe(true);
  });

  test("defaults to insecure in development, where there is no certificate", () => {
    setEnv("COOKIE_SECURE");
    setEnv("NODE_ENV", "development");

    expect(cookieSecure()).toBe(false);
  });

  test("treats an unreadable value as unset rather than as false", () => {
    // Falling back to the safe default beats reading "yes" as false and
    // dropping the attribute in production, which is the failure worth
    // designing out.
    setEnv("COOKIE_SECURE", "yes please");
    setEnv("NODE_ENV", "production");

    expect(cookieSecure()).toBe(true);
  });
});

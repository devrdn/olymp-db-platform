import { afterEach, describe, expect, test } from "vitest";

import { cookieSecure } from "./cookie-policy";

const original = { ...process.env };

// NODE_ENV is typed read-only; these tests set it to make build and deployment disagree.
const setEnv = (key: string, value?: string) => {
  const env = process.env as Record<string, string | undefined>;
  if (value === undefined) delete env[key];
  else env[key] = value;
};

afterEach(() => {
  process.env = { ...original };
});

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
    setEnv("COOKIE_SECURE", "yes please");
    setEnv("NODE_ENV", "production");

    expect(cookieSecure()).toBe(true);
  });
});

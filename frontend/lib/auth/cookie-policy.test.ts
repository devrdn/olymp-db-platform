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

  test("treats an empty value as unset, as the API does", () => {
    setEnv("COOKIE_SECURE", "");
    setEnv("NODE_ENV", "production");

    expect(cookieSecure()).toBe(true);
  });

  // The API reads the variable with Go's strconv.ParseBool, and the cookie's
  // name follows it on both sides.
  test.each(["1", "t", "T", "TRUE", "true", "True"])("reads %s as true, as the API does", (value) => {
    setEnv("COOKIE_SECURE", value);
    setEnv("NODE_ENV", "development");

    expect(cookieSecure()).toBe(true);
  });

  test.each(["0", "f", "F", "FALSE", "false", "False"])("reads %s as false, as the API does", (value) => {
    setEnv("COOKIE_SECURE", value);
    setEnv("NODE_ENV", "production");

    expect(cookieSecure()).toBe(false);
  });

  test.each(["yes please", "tRuE", " true", "on"])(
    "refuses %j, which the API refuses to start with, rather than guess a cookie name",
    (value) => {
      setEnv("COOKIE_SECURE", value);
      setEnv("NODE_ENV", "production");

      expect(() => cookieSecure()).toThrow(/COOKIE_SECURE/);
    },
  );
});

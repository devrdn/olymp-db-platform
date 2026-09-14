import { afterEach, describe, expect, test, vi } from "vitest";

import { apiOrigin, ingressSecret } from "./config";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("apiOrigin", () => {
  test("answers with the configured origin", () => {
    vi.stubEnv("API_ORIGIN", "http://api:8080");

    expect(apiOrigin()).toBe("http://api:8080");
  });

  test("falls back to the local stack in development, where that is the address", () => {
    vi.stubEnv("API_ORIGIN", "");
    vi.stubEnv("NODE_ENV", "development");

    expect(apiOrigin()).toBe("http://localhost:8080");
  });

  test("refuses to guess in production, naming the variable that is missing", () => {
    vi.stubEnv("API_ORIGIN", "");
    vi.stubEnv("NODE_ENV", "production");

    expect(() => apiOrigin()).toThrowError(/API_ORIGIN/);
  });
});

/**
 * The secret the reverse proxy proves itself with (see lib/api/forwarded.ts).
 * Without it no forwarded address is handed to the API — the safe answer, and
 * one that makes every visitor look like the web container. In production that
 * is almost certainly a deployment that forgot the variable, so it is said out
 * loud; in development there is usually no proxy at all, and nothing to say.
 */
describe("ingressSecret", () => {
  const SECRET = "an-ingress-secret-of-at-least-32-characters";

  test("answers with a configured secret long enough to mean something", () => {
    vi.stubEnv("INGRESS_SECRET", SECRET);

    expect(ingressSecret()).toBe(SECRET);
  });

  test.each([
    ["missing", ""],
    ["too short", "short"],
  ])("answers with no secret when it is %s", (_, value) => {
    vi.stubEnv("INGRESS_SECRET", value);
    vi.stubEnv("NODE_ENV", "development");
    vi.spyOn(console, "error").mockImplementation(() => {});

    expect(ingressSecret()).toBeNull();
  });

  test("says so in production, once, without repeating the value", async () => {
    // A fresh module: the report is once per process.
    vi.resetModules();
    const fresh = await import("./config");
    vi.stubEnv("INGRESS_SECRET", "short-and-secret");
    vi.stubEnv("NODE_ENV", "production");
    const error = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(fresh.ingressSecret()).toBeNull();
    fresh.ingressSecret();
    expect(error).toHaveBeenCalledTimes(1);
    const said = error.mock.calls.flat().join(" ");
    expect(said).toMatch(/INGRESS_SECRET/);
    expect(said).not.toContain("short-and-secret");
  });

  test("keeps quiet in development", () => {
    vi.stubEnv("INGRESS_SECRET", "");
    vi.stubEnv("NODE_ENV", "development");
    const error = vi.spyOn(console, "error").mockImplementation(() => {});

    ingressSecret();

    expect(error).not.toHaveBeenCalled();
  });
});

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

  test("refuses the example file's placeholder in production, without repeating it", async () => {
    // A "change-me" placeholder from deploy/.env.example is public, so it vouches for nobody.
    vi.resetModules();
    const fresh = await import("./config");
    const placeholder = "CHANGE-ME-to-the-output-of-openssl-rand-hex-32";
    vi.stubEnv("INGRESS_SECRET", placeholder);
    vi.stubEnv("NODE_ENV", "production");
    const error = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(fresh.ingressSecret()).toBeNull();
    const said = error.mock.calls.flat().join(" ");
    expect(said).toMatch(/INGRESS_SECRET/);
    expect(said.toLowerCase()).not.toContain("change-me");
  });

  test("lets development keep the placeholder", () => {
    vi.stubEnv("INGRESS_SECRET", "change-me-to-the-output-of-openssl-rand-hex-32");
    vi.stubEnv("NODE_ENV", "development");

    expect(ingressSecret()).toBe("change-me-to-the-output-of-openssl-rand-hex-32");
  });

  test("keeps quiet in development", () => {
    vi.stubEnv("INGRESS_SECRET", "");
    vi.stubEnv("NODE_ENV", "development");
    const error = vi.spyOn(console, "error").mockImplementation(() => {});

    ingressSecret();

    expect(error).not.toHaveBeenCalled();
  });
});

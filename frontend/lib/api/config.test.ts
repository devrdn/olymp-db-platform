import { afterEach, describe, expect, test, vi } from "vitest";

import { apiOrigin } from "./config";

afterEach(() => {
  vi.unstubAllEnvs();
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

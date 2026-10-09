import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Without a proxy, browser requests to `/api/*` (the settings images) reach
 * Next, which does not serve them; the rewrite provides the single origin.
 */
describe("rewrites", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  async function rewritesFor(nodeEnv: string, apiOrigin?: string) {
    vi.stubEnv("NODE_ENV", nodeEnv);
    vi.stubEnv("API_ORIGIN", apiOrigin ?? "");
    const { default: config } = await import("./next.config");
    return config.rewrites ? await config.rewrites() : [];
  }

  it("sends the browser's API calls to the API in development", async () => {
    const rules = await rewritesFor("development");

    expect(rules).toEqual([
      { source: "/api/v1/:path*", destination: "http://localhost:8080/api/v1/:path*" },
    ]);
  });

  it("honours a configured origin rather than assuming the local stack", async () => {
    const rules = await rewritesFor("development", "http://api:8080");

    expect(rules).toEqual([
      { source: "/api/v1/:path*", destination: "http://api:8080/api/v1/:path*" },
    ]);
  });

  /**
   * A production build may run without a proxy (`make front-start`), so the
   * rule is not gated on the environment.
   */
  it("carries the route in production too, where a proxy may not be in front", async () => {
    const rules = await rewritesFor("production", "http://api:8080");

    expect(rules).toEqual([
      { source: "/api/v1/:path*", destination: "http://api:8080/api/v1/:path*" },
    ]);
  });

  /** At build time the address is unknown; throwing would fail every build. */
  it("adds no rule when the address is unknown, rather than refusing to build", async () => {
    const rules = await rewritesFor("production");

    expect(rules).toEqual([]);
  });
});

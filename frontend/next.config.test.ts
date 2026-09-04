import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The browser reaches the API through one origin.
 *
 * In production the reverse proxy owns `/api/*` and the application never sees
 * it. A development stack has no proxy, so anything the *browser* asks for
 * under that prefix lands on the Next server, which does not serve it: the
 * settings images are the only place the page fetches from the API directly,
 * and they answered with this application's own HTML 404 instead of the
 * picture. The rewrite gives development the single origin production gets for
 * free.
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
   * `make front-start` serves a production build with no proxy in front of it,
   * so gating this on the environment would have left the very case that
   * reported the bug still broken. Behind a proxy the rule is inert: the proxy
   * takes `/api/*` before Next sees it.
   */
  it("carries the route in production too, where a proxy may not be in front", async () => {
    const rules = await rewritesFor("production", "http://api:8080");

    expect(rules).toEqual([
      { source: "/api/v1/:path*", destination: "http://api:8080/api/v1/:path*" },
    ]);
  });

  /**
   * This also runs at build time, where nothing has told the interface where
   * the API is and nothing needs to know: a build that threw here would fail
   * every CI run, which is exactly what the first attempt at this did.
   */
  it("adds no rule when the address is unknown, rather than refusing to build", async () => {
    const rules = await rewritesFor("production");

    expect(rules).toEqual([]);
  });
});

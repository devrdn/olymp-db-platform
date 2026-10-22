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

describe("headers", () => {
  // Reset per call, not per test: `isDevelopment` is read once at import, and
  // a test compares both environments.
  async function headersFor(nodeEnv: string) {
    vi.resetModules();
    vi.stubEnv("NODE_ENV", nodeEnv);
    const { default: config } = await import("./next.config");
    const rules = config.headers ? await config.headers() : [];
    return Object.fromEntries(rules.flatMap((rule) => rule.headers.map((h) => [h.key, h.value])));
  }

  /**
   * The development server is plain http, and a policy that pinned localhost
   * to https would outlive the process that sent it.
   */
  it("pins https for a year, with subdomains, in production only", async () => {
    expect((await headersFor("production"))["Strict-Transport-Security"]).toBe(
      "max-age=31536000; includeSubDomains",
    );
    expect(await headersFor("development")).not.toHaveProperty("Strict-Transport-Security");
  });

  it("keeps the content policy beside it, and only there", async () => {
    expect(await headersFor("production")).toHaveProperty("Content-Security-Policy");
    expect(await headersFor("development")).not.toHaveProperty("Content-Security-Policy");
  });
});

describe("the server itself", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  /** `/_next/image` decodes bytes it is pointed at; nothing here renders `next/image`. */
  it("mounts no image optimizer", async () => {
    const { default: config } = await import("./next.config");

    expect(config.images?.unoptimized).toBe(true);
  });

  /** The reverse proxy compresses (deploy/Caddyfile); Node must not gzip first. */
  it("leaves compression to the reverse proxy", async () => {
    const { default: config } = await import("./next.config");

    expect(config.compress).toBe(false);
  });
});

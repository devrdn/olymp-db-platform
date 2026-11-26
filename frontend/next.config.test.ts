import * as pageStaticInfo from "next/dist/build/analysis/get-page-static-info";
import { getPathMatch } from "next/dist/shared/lib/router/utils/path-match";
import { compileNonPath } from "next/dist/shared/lib/router/utils/prepare-destination";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { STATIC_CONTENT_SECURITY_POLICY, UNPROXIED_PATHS } from "./lib/csp";
import { config as proxyConfig } from "./proxy";

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

  /** A reset link carries its token in the address; no request from that page may repeat it. */
  it("sends no referrer at all from the reset page, in every environment", async () => {
    for (const env of ["production", "development"]) {
      vi.resetModules();
      vi.stubEnv("NODE_ENV", env);
      const { default: config } = await import("./next.config");
      const rules = config.headers ? await config.headers() : [];
      const reset = rules.filter((rule) => rule.source === "/reset");

      expect(reset.flatMap((rule) => rule.headers)).toContainEqual({ key: "Referrer-Policy", value: "no-referrer" });
      // Later rules win, so the general one must not come after it.
      expect(rules.indexOf(reset[0])).toBe(rules.length - 1);
    }
  });

  it("sends no content policy in development", async () => {
    const sent = Object.keys(await headersFor("development")).map((key) => key.toLowerCase());

    expect(sent).not.toContain("content-security-policy");
  });
});

/**
 * A second policy beside the proxy's would be enforced too and block the
 * nonced scripts, and a path the proxy skips must still get one. Both sides
 * are compiled the way Next compiles them.
 */
describe("the content policy's two sources", () => {
  // Not in Next's published typings.
  const { getMiddlewareMatchers } = pageStaticInfo as unknown as {
    getMiddlewareMatchers: (matchers: string[], nextConfig: object) => { regexp: string }[];
  };
  const proxyRuns = (path: string) =>
    getMiddlewareMatchers(proxyConfig.matcher, {}).some(({ regexp }) => new RegExp(regexp).test(path));

  /**
   * As Next's router applies a header rule: matched case-insensitively, and
   * each value compiled against the parameters the match captured.
   */
  async function rulesAt(path: string, nodeEnv = "production") {
    vi.resetModules();
    vi.stubEnv("NODE_ENV", nodeEnv);
    const { default: config } = await import("./next.config");
    const rules = config.headers ? await config.headers() : [];
    return rules.flatMap((rule) => {
      const params = getPathMatch(rule.source, { strict: true, removeUnnamedParams: true, sensitive: false })(path);
      if (!params) return [];
      const compile = (value: string) => (Object.keys(params).length > 0 ? compileNonPath(value, params) : value);
      return [{ params, headers: rule.headers.map((header) => ({ key: header.key, value: compile(header.value) })) }];
    });
  }

  async function policiesAt(path: string, nodeEnv = "production") {
    return (await rulesAt(path, nodeEnv))
      .flatMap((rule) => rule.headers)
      .filter((header) => header.key.toLowerCase() === "content-security-policy")
      .map((header) => header.value);
  }

  it("captures no parameter, so Next sends the static policy as written", async () => {
    const [rule] = (await rulesAt("/robots.txt")).filter((matched) =>
      matched.headers.some((header) => header.key === "content-security-policy"),
    );

    expect(rule.params).toEqual({});
    expect(rule.headers).toEqual([{ key: "content-security-policy", value: STATIC_CONTENT_SECURITY_POLICY }]);
  });

  it("holds the proxy's matcher to the list the static policy is sent on", () => {
    expect(proxyConfig.matcher).toEqual([`/((?!${UNPROXIED_PATHS.join("|")}).*)`]);
  });

  it.each([
    "/robots.txt",
    "/sitemap.xml",
    "/favicon.ico",
    "/icon.svg",
    "/apple-icon.png",
    "/brand/google-g.png",
    "/brand/no-such-mark",
    "/_next/static/chunks/main.js",
  ])("sends the static policy on %s, where the proxy does not run", async (path) => {
    expect(proxyRuns(path)).toBe(false);
    expect(await policiesAt(path)).toEqual([STATIC_CONTENT_SECURITY_POLICY]);
  });

  it.each(["/", "/login", "/contests/1/play", "/brands", "/brand", "/users/8f3a.tar.gz", "/api/v1/settings"])(
    "sends no policy of its own on %s, where the proxy runs and sends one",
    async (path) => {
      expect(proxyRuns(path)).toBe(true);
      expect(await policiesAt(path)).toEqual([]);
    },
  );

  // The overlap next.config.ts describes, where the proxy's policy is applied last.
  it.each(["/Brand/x", "/ROBOTS.TXT"])("meets both on %s, a spelling the proxy still runs on", async (path) => {
    expect(proxyRuns(path)).toBe(true);
    expect(await policiesAt(path)).toEqual([STATIC_CONTENT_SECURITY_POLICY]);
  });

  it("sends no static policy in development, where the proxy sends none either", async () => {
    expect(await policiesAt("/robots.txt", "development")).toEqual([]);
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

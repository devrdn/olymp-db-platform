import { NextRequest } from "next/server";
import { afterEach, describe, expect, test, vi } from "vitest";

import { INGRESS_HEADER } from "@/lib/api/forwarded";
import { SESSION_COOKIE } from "@/lib/auth/session";

import { config, proxy } from "./proxy";

/**
 * These start from a request, as the runtime does. `guardRedirect`'s own tests
 * build their own argument, so they could not catch the proxy joining path and
 * query, which made `/login?next=…` redirect to itself.
 */
function request(url: string, session = false) {
  const req = new NextRequest(new URL(url, "http://localhost:3000"));
  if (session) req.cookies.set(SESSION_COOKIE, "opaque");
  return req;
}

/** Where the proxy redirects this request, or null if it passes. */
function destination(url: string, session = false): string | null {
  const response = proxy(request(url, session));
  return response.headers.get("location");
}

describe("proxy", () => {
  test("sends a signed-out visitor to sign-in", () => {
    expect(destination("/contests")).toBe("http://localhost:3000/login?next=%2Fcontests");
  });

  test("lets a signed-in visitor through", () => {
    expect(destination("/contests", true)).toBeNull();
  });

  // The healthcheck has no session and treats a redirect as failure.
  test("lets a signed-out liveness probe through, setting no cookie", () => {
    const response = proxy(request("/healthz"));
    expect(response.headers.get("location")).toBeNull();
    expect(response.headers.get("set-cookie")).toBeNull();
  });

  /**
   * Sign-in must stay reachable while carrying `next`, or the guard redirects
   * its own destination.
   */
  test("does not redirect the page it redirects to", () => {
    expect(destination("/login?next=%2Fcontests")).toBeNull();
  });

  test("terminates: following its own redirect reaches a page that stays put", () => {
    // A walk, because the failure was a sequence that never ended.
    let url = "/contests";
    const seen = [url];

    for (let hop = 0; hop < 5; hop += 1) {
      const next = destination(url);
      if (next === null) break;
      url = new URL(next).pathname + new URL(next).search;
      seen.push(url);
    }

    expect(seen).toEqual(["/contests", "/login?next=%2Fcontests"]);
  });

  test("carries a search along, so signing in resumes the exact view", () => {
    expect(destination("/users?q=popescu&status=blocked")).toBe(
      "http://localhost:3000/login?next=%2Fusers%3Fq%3Dpopescu%26status%3Dblocked",
    );
  });
});

/**
 * An unvouched forwarded address is removed on every path: the rewrite matches
 * `/api` case-insensitively, so a lower-case-only check let `/API/...` through.
 */
describe("proxy's forwarded headers", () => {
  const SECRET = "an-ingress-secret-of-at-least-32-characters";

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  function passedOn(path: string, headers: Record<string, string>, session = true) {
    const req = new NextRequest(new URL(path, "http://localhost:3000"), { headers });
    if (session) req.cookies.set(SESSION_COOKIE, "opaque");
    const response = proxy(req);
    const overridden = (response.headers.get("x-middleware-override-headers") ?? "").split(",");
    return {
      redirect: response.headers.get("location"),
      has: (name: string) => overridden.includes(name),
      value: (name: string) => response.headers.get(`x-middleware-request-${name}`),
    };
  }

  const forged = {
    "x-forwarded-for": "10.20.30.40",
    "x-real-ip": "10.20.30.41",
    forwarded: "for=10.20.30.42",
    accept: "text/html",
  };

  test.each(["/api/v1/auth/login", "/API/v1/auth/login", "/Api/V1/auth/login", "/contests", "/login"])(
    "removes a forwarded address the browser wrote itself on %s",
    (path) => {
      vi.stubEnv("INGRESS_SECRET", SECRET);

      const out = passedOn(path, forged);

      expect(out.redirect).toBeNull();
      for (const name of ["x-forwarded-for", "x-real-ip", "forwarded"]) {
        expect(out.has(name)).toBe(false);
        expect(out.value(name)).toBeNull();
      }
      expect(out.value("accept")).toBe("text/html");
    },
  );

  test("removes it too when this server has no secret configured", () => {
    vi.stubEnv("INGRESS_SECRET", "");

    const out = passedOn("/API/v1/auth/login", { ...forged, [INGRESS_HEADER]: "" });

    expect(out.has("x-forwarded-for")).toBe(false);
  });

  test.each(["/api/v1/settings/images/logo", "/API/v1/settings/images/logo"])(
    "does not send a signed-out browser to sign-in on %s: the API guards its own",
    (path) => {
      vi.stubEnv("INGRESS_SECRET", SECRET);

      expect(passedOn(path, {}, false).redirect).toBeNull();
    },
  );

  test.each(["/api/v1/settings", "/API/v1/settings"])(
    "keeps a vouched address on %s and drops the secret before the API",
    (path) => {
      vi.stubEnv("INGRESS_SECRET", SECRET);

      const out = passedOn(path, { "x-forwarded-for": "203.0.113.7", [INGRESS_HEADER]: SECRET });

      expect(out.value("x-forwarded-for")).toBe("203.0.113.7");
      expect(out.has(INGRESS_HEADER)).toBe(false);
    },
  );

  test("keeps a vouched address and its secret on a screen, where the server checks it again", () => {
    // lib/api/forwarded.ts checks the secret when forwarding; removing it here
    // would unvouch every request.
    vi.stubEnv("INGRESS_SECRET", SECRET);

    const out = passedOn("/contests", { "x-forwarded-for": "203.0.113.7", [INGRESS_HEADER]: SECRET });

    expect(out.value("x-forwarded-for")).toBe("203.0.113.7");
    expect(out.value(INGRESS_HEADER)).toBe(SECRET);
  });
});

/**
 * `config.matcher` is a string the framework compiles, so these test it as the
 * regular expression it is.
 */
describe("proxy's matcher", () => {
  const pattern = new RegExp(`^${config.matcher[0]}$`);

  test.each([
    ["/_next/static/chunk.js", "the framework's own asset"],
    ["/favicon.ico", "an asset, not a screen"],
  ])("leaves %s alone — %s", (path) => {
    expect(pattern.test(path)).toBe(false);
  });

  test.each([
    ["/", "a screen"],
    ["/contests", "a screen"],
    ["/users/8f3a.tar.gz", "a screen whose path happens to carry dots"],
    ["/api/v1/settings", "the API's: its forwarded address is checked, sign-in is not"],
    ["/api/v1/settings/images/logo", "public on purpose, and still passed through the same check"],
  ])("runs on %s — %s", (path) => {
    // Named exclusions: "anything with a dot" would silently skip such routes.
    expect(pattern.test(path)).toBe(true);
  });
});

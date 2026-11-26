import {
  getScriptNonceFromHeader,
  getScriptNonceFromHeaders,
} from "next/dist/server/app-render/get-script-nonce-from-header";
import { describe, expect, test } from "vitest";

import { contentSecurityPolicy, NONCE_REQUEST_HEADERS, newNonce, STATIC_CONTENT_SECURITY_POLICY } from "./csp";

describe("contentSecurityPolicy", () => {
  test("is the whole policy, scripts allowed only by the nonce and what they load", () => {
    expect(contentSecurityPolicy("N")).toBe(
      "default-src 'self'; script-src 'self' 'nonce-N' 'strict-dynamic'; style-src 'self' 'unsafe-inline'; " +
        "img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; form-action 'self'; " +
        "frame-ancestors 'none'; base-uri 'none'; object-src 'none'",
    );
  });

  test("hands Next the nonce it carries", () => {
    const nonce = newNonce();

    expect(getScriptNonceFromHeader(contentSecurityPolicy(nonce))).toBe(nonce);
  });
});

describe("STATIC_CONTENT_SECURITY_POLICY", () => {
  test("is the same policy with scripts from this origin only and no inline script at all", () => {
    expect(STATIC_CONTENT_SECURITY_POLICY).toBe(contentSecurityPolicy("N").replace(" 'nonce-N' 'strict-dynamic'", ""));
    expect(getScriptNonceFromHeader(STATIC_CONTENT_SECURITY_POLICY)).toBeUndefined();
  });
});

describe("NONCE_REQUEST_HEADERS", () => {
  /** A header missing from the list would let a browser choose the nonce Next stamps. */
  test.each(NONCE_REQUEST_HEADERS)("names %s, a header Next takes the nonce from", (name) => {
    const nonce = newNonce();

    expect(getScriptNonceFromHeaders({ [name]: contentSecurityPolicy(nonce) })).toBe(nonce);
  });

  test("names both headers Next reads", () => {
    expect([...NONCE_REQUEST_HEADERS].sort()).toEqual([
      "content-security-policy",
      "content-security-policy-report-only",
    ]);
  });
});

describe("newNonce", () => {
  test("is sixteen random bytes in standard base64", () => {
    const nonce = newNonce();

    expect(nonce).toMatch(/^[A-Za-z0-9+/]{22}==$/);
    expect(Buffer.from(nonce, "base64")).toHaveLength(16);
  });

  test("differs on every call", () => {
    expect(newNonce()).not.toBe(newNonce());
  });
});

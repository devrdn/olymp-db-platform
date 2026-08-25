import { describe, expect, test } from "vitest";

import { ApiError } from "@/lib/api/client";

import { expiredSessionRedirect, guardRedirect } from "./guard";

describe("guardRedirect", () => {
  test("sends a signed-out visitor to sign-in, remembering where they were going", () => {
    expect(guardRedirect("/contests", false)).toBe("/login?next=%2Fcontests");
  });

  test("leaves sign-in reachable without a session", () => {
    expect(guardRedirect("/login", false)).toBeNull();
  });

  test("lets a signed-in visitor through", () => {
    expect(guardRedirect("/contests", true)).toBeNull();
  });
});

/**
 * The guard can only see whether a cookie exists; the cookie is httpOnly and
 * only the API can say whether it is still valid. So a stale session reaches
 * the page and comes back as `unauthenticated` — and the recoverable-error
 * screen then offers a retry that can never succeed, because nothing about
 * signing in happens by asking again.
 */
describe("expiredSessionRedirect", () => {
  test("sends a dead session back to sign in, carrying where it was going", () => {
    expect(expiredSessionRedirect(new ApiError("unauthenticated", 401, "..."), "/contests")).toBe(
      "/login?next=%2Fcontests",
    );
  });

  test("leaves a failure that a retry could fix alone", () => {
    expect(
      expiredSessionRedirect(new ApiError("internal_error", 500, "..."), "/contests"),
    ).toBeNull();
    expect(expiredSessionRedirect(new ApiError("unreachable", 502, "..."), "/contests")).toBeNull();
  });

  test("leaves a refusal that signing in again would not lift", () => {
    // The account is signed in and simply not allowed: sending it to the form
    // would loop it straight back here.
    expect(expiredSessionRedirect(new ApiError("forbidden", 403, "..."), "/contests")).toBeNull();
  });

  test("ignores anything that is not an API failure", () => {
    expect(expiredSessionRedirect(new Error("boom"), "/contests")).toBeNull();
  });
});

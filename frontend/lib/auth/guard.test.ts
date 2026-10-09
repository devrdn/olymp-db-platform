import { describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

import { authRecoveryRedirect, guardRedirect } from "./guard";

describe("guardRedirect", () => {
  test("sends a signed-out visitor to sign-in, remembering where they were going", () => {
    expect(guardRedirect("/contests", "", false)).toBe("/login?next=%2Fcontests");
  });

  test("leaves sign-in reachable without a session", () => {
    expect(guardRedirect("/login", "", false)).toBeNull();
  });

  test("the front page is open to a visitor without a session", () => {
    expect(guardRedirect("/", "", false)).toBeNull();
  });

test("does not let a doubled slash walk in through the front page", () => {
  expect(guardRedirect("//my", "", false)).not.toBeNull();
});

test("and it is still only the front page that is open", () => {
    expect(guardRedirect("/my", "", false)).toBe("/login?next=%2Fmy");
  });

  test("leaves the liveness route reachable without a session", () => {
    expect(guardRedirect("/healthz", "", false)).toBeNull();
  });

  test("opens the liveness route itself and nothing beneath it", () => {
    expect(guardRedirect("/healthz/anything", "", false)).toBe("/login?next=%2Fhealthz%2Fanything");
  });

  test("leaves a contest's public table reachable without a session", () => {
    expect(guardRedirect("/contests/3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6/leaderboard", "", false)).toBeNull();
  });

  test("opens exactly the table, and nothing beside or beneath it", () => {
    for (const path of [
      "/contests/3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6/play",
      "/contests/3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6/leaderboard/live",
      "/contests/3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6/standings",
      "/contests/a/b/leaderboard",
      "/contests//leaderboard",
      "/contests",
    ]) {
      expect(guardRedirect(path, "", false), path).toBe(`/login?next=${encodeURIComponent(path)}`);
    }
  });

  test("lets a signed-in visitor through", () => {
    expect(guardRedirect("/contests", "", true)).toBeNull();
  });

  test("keeps the password screen behind a session", () => {
    expect(guardRedirect("/password", "", false)).toBe("/login?next=%2Fpassword");
  });

  // The redirect itself lands on `/login?next=…`; matching with the query
  // attached would loop.
  test("leaves sign-in reachable when it carries where to go next", () => {
    expect(guardRedirect("/login", "?next=%2Fcontests", false)).toBeNull();
  });

  test("carries the query along, so a search survives signing in", () => {
    expect(guardRedirect("/users", "?q=popescu", false)).toBe(
      "/login?next=%2Fusers%3Fq%3Dpopescu",
    );
  });
});

describe("authRecoveryRedirect", () => {
  test("sends a dead session back to sign in, carrying where it was going", () => {
    expect(authRecoveryRedirect(new ApiError("unauthenticated", 401, "..."), "/contests")).toBe(
      "/login?next=%2Fcontests",
    );
  });

  test("sends an account still on its one-time password to the one screen it may use", () => {
    expect(
      authRecoveryRedirect(new ApiError("password_change_required", 403, "..."), "/contests"),
    ).toBe("/password");
  });

  test("does not carry a destination into the password screen", () => {
    expect(
      authRecoveryRedirect(new ApiError("password_change_required", 403, "..."), "/contests"),
    ).not.toContain("next=");
  });

  test("leaves a failure that a retry could fix alone", () => {
    expect(authRecoveryRedirect(new ApiError("internal_error", 500, "..."), "/contests")).toBeNull();
    expect(authRecoveryRedirect(new ApiError("unreachable", 502, "..."), "/contests")).toBeNull();
  });

  test("leaves a refusal that signing in again would not lift", () => {
    expect(authRecoveryRedirect(new ApiError("forbidden", 403, "..."), "/contests")).toBeNull();
  });

  test("ignores anything that is not an API failure", () => {
    expect(authRecoveryRedirect(new Error("boom"), "/contests")).toBeNull();
  });
});

describe("authRecoveryRedirect, leaving a trace", () => {
  test("records which code sent the visitor back, and where from", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    authRecoveryRedirect(new ApiError("unauthenticated", 401, "Sign in", "req-42"), "/contests");

    expect(warn).toHaveBeenCalledTimes(1);
    const line = warn.mock.calls[0].join(" ");
    expect(line).toContain("unauthenticated");
    expect(line).toContain("/contests");
    expect(line).toContain("req-42");

    warn.mockRestore();
  });

  test("says nothing when it is not the one redirecting", () => {
    // Logging `forbidden` would add a line for every ordinary permission check.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    authRecoveryRedirect(new ApiError("forbidden", 403, "No", "req-7"), "/contests");

    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});

describe("guardRedirect, resuming the exact view", () => {
  test("carries the query string, not just the path", () => {
    expect(guardRedirect("/users", "?q=popescu&status=blocked", false)).toBe(
      "/login?next=%2Fusers%3Fq%3Dpopescu%26status%3Dblocked",
    );
  });

  test("leaves a plain path exactly as it was", () => {
    expect(guardRedirect("/contests", "", false)).toBe("/login?next=%2Fcontests");
  });
});

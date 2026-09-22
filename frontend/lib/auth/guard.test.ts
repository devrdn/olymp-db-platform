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

  test("and it is still only the front page that is open", () => {
    expect(guardRedirect("/my", "", false)).toBe("/login?next=%2Fmy");
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
    // It is reached by an account that is signed in and stuck; without a
    // session there is nothing to change.
    expect(guardRedirect("/password", "", false)).toBe("/login?next=%2Fpassword");
  });

  /**
   * The redirect this guard issues carries a query of its own, so the next
   * request arrives at `/login?next=…` rather than at `/login`. If the
   * allow-list is consulted with the query attached, sign-in stops matching
   * it and the guard sends the sign-in page to itself, wrapping `next` one
   * encoding deeper each time — a loop the browser ends with
   * ERR_TOO_MANY_REDIRECTS, and which locks out every signed-out visitor
   * rather than some unlucky path.
   *
   * Hence two arguments: the path decides, the query is only carried. They
   * are separate parameters so that the decision cannot be handed a query
   * string again by accident.
   */
  test("leaves sign-in reachable when it carries where to go next", () => {
    expect(guardRedirect("/login", "?next=%2Fcontests", false)).toBeNull();
  });

  test("carries the query along, so a search survives signing in", () => {
    expect(guardRedirect("/users", "?q=popescu", false)).toBe(
      "/login?next=%2Fusers%3Fq%3Dpopescu",
    );
  });
});

/**
 * The guard can only see whether a cookie exists; the cookie is httpOnly and
 * only the API can say what it is still worth. So the real verdict arrives
 * after the page has been asked to render, as a failure code — and the
 * recoverable-error screen then offers a retry that can never succeed, because
 * neither signing in nor replacing a password happens by asking again.
 */
describe("authRecoveryRedirect", () => {
  test("sends a dead session back to sign in, carrying where it was going", () => {
    expect(authRecoveryRedirect(new ApiError("unauthenticated", 401, "..."), "/contests")).toBe(
      "/login?next=%2Fcontests",
    );
  });

  test("sends an account still on its one-time password to the one screen it may use", () => {
    // The API closes everything except the way out. The interface has to agree,
    // or the account meets an error screen on every route instead of the form
    // that unblocks it.
    expect(
      authRecoveryRedirect(new ApiError("password_change_required", 403, "..."), "/contests"),
    ).toBe("/password");
  });

  test("does not carry a destination into the password screen", () => {
    // Nothing resumes here: changing a password retires every session, so the
    // journey restarts at sign-in whatever they were doing.
    expect(
      authRecoveryRedirect(new ApiError("password_change_required", 403, "..."), "/contests"),
    ).not.toContain("next=");
  });

  test("leaves a failure that a retry could fix alone", () => {
    expect(authRecoveryRedirect(new ApiError("internal_error", 500, "..."), "/contests")).toBeNull();
    expect(authRecoveryRedirect(new ApiError("unreachable", 502, "..."), "/contests")).toBeNull();
  });

  test("leaves a refusal that signing in again would not lift", () => {
    // The account is signed in and simply not allowed: sending it to the form
    // would loop it straight back here.
    expect(authRecoveryRedirect(new ApiError("forbidden", 403, "..."), "/contests")).toBeNull();
  });

  test("ignores anything that is not an API failure", () => {
    expect(authRecoveryRedirect(new Error("boom"), "/contests")).toBeNull();
  });
});

describe("authRecoveryRedirect, leaving a trace", () => {
  /**
   * The bug this exists for was reported twice and reproduced neither time:
   * a session that bounces to the sign-in form, with nothing on either side
   * saying which request was refused or why. The redirect is the moment the
   * interface knows, and it was the one moment that said nothing.
   */
  test("records which code sent the visitor back, and where from", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    authRecoveryRedirect(new ApiError("unauthenticated", 401, "Sign in", "req-42"), "/contests");

    expect(warn).toHaveBeenCalledTimes(1);
    const line = warn.mock.calls[0].join(" ");
    expect(line).toContain("unauthenticated");
    expect(line).toContain("/contests");
    // The request id is what ties this line to the API's own log for the very
    // same request, which is the whole point of writing one.
    expect(line).toContain("req-42");

    warn.mockRestore();
  });

  test("says nothing when it is not the one redirecting", () => {
    // `forbidden` is somebody signed in and simply not allowed. Logging it
    // here would put a line in the log for every ordinary permission check.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    authRecoveryRedirect(new ApiError("forbidden", 403, "No", "req-7"), "/contests");

    expect(warn).not.toHaveBeenCalled();
    warn.mockRestore();
  });
});

describe("guardRedirect, resuming the exact view", () => {
  /**
   * These two once called the guard with the path and query joined into one
   * string, which is how the proxy called it, and they passed — while every
   * signed-out visitor was caught in a redirect loop. The function was right
   * about the string it was handed; the bug was in what it was handed, and a
   * test that builds the argument itself cannot see that. Hence the separate
   * parameters, which put the mistake beyond the type checker's tolerance.
   */
  test("carries the query string, not just the path", () => {
    // The promise this function makes is that signing in resumes the journey.
    // A filtered register, a page of results, a search somebody typed — all of
    // that lives in the query string, and dropping it lands them on a bare
    // list wondering what happened to their search.
    expect(guardRedirect("/users", "?q=popescu&status=blocked", false)).toBe(
      "/login?next=%2Fusers%3Fq%3Dpopescu%26status%3Dblocked",
    );
  });

  test("leaves a plain path exactly as it was", () => {
    expect(guardRedirect("/contests", "", false)).toBe("/login?next=%2Fcontests");
  });
});

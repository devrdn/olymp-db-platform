/**
 * Route protection, decided from the path and whether a session cookie exists.
 *
 * The cookie's presence is all this layer can know: it is httpOnly and only
 * the API can say whether it is still valid. That is deliberate. A cheap check
 * here keeps signed-out visitors off the constructor, and the API remains the
 * one that actually decides, answering `unauthenticated` if the cookie is
 * stale.
 */

import { ApiError } from "@/lib/api/client";

/** Reachable without a session, because they are how a session is obtained. */
const PUBLIC_PATHS = ["/login"];

/** Where a visitor goes to obtain one. */
const SIGN_IN = "/login";

/** The one screen an account still on its one-time password may use. */
const PASSWORD_CHANGE = "/password";

export function guardRedirect(pathname: string, hasSession: boolean): string | null {
  if (hasSession) return null;
  if (PUBLIC_PATHS.some((path) => pathname === path || pathname.startsWith(`${path}/`))) {
    return null;
  }

  // Where they were going is carried along, so signing in resumes the journey
  // instead of dropping them on a landing page.
  return `/login?next=${encodeURIComponent(pathname)}`;
}

/**
 * The other half of the guard, for what it cannot see.
 *
 * `guardRedirect` knows only that a cookie exists. What that cookie is still
 * worth is the API's answer, and it arrives after the page has already been
 * asked to render. Left alone, both verdicts below land on the
 * recoverable-error screen, which offers a retry — and neither signing in nor
 * replacing a password happens by asking the same question again, so the retry
 * is a button that cannot work.
 *
 * Two codes, two destinations:
 *
 * - `unauthenticated` — the session is gone. Back to the form, carrying where
 *   they were going, so signing in resumes the journey.
 * - `password_change_required` — the session is fine and the account is still
 *   on the password an administrator handed it. The API closes every other
 *   endpoint until it is replaced, so the interface has exactly one screen to
 *   offer. Nothing is carried: the change retires every session, and the
 *   journey restarts at sign-in regardless.
 *
 * `forbidden` is deliberately not included. That account is signed in and
 * simply not allowed, and the sign-in form would bounce it straight back.
 *
 * Every redirect from here is recorded. Being returned to the sign-in form
 * with a session that looked fine was reported twice and reproduced neither
 * time, and this was the reason: the moment the interface knew which request
 * had been refused, and why, was the one moment it said nothing. The line
 * carries the API's own request id, so it names the matching entry in the
 * server's log rather than merely agreeing that something went wrong.
 */
export function authRecoveryRedirect(error: unknown, pathname: string): string | null {
  if (!(error instanceof ApiError)) return null;

  const target =
    error.code === "password_change_required"
      ? PASSWORD_CHANGE
      : error.code === "unauthenticated"
        ? `${SIGN_IN}?next=${encodeURIComponent(pathname)}`
        : null;

  if (target) {
    console.warn(
      `auth redirect: ${pathname} -> ${target} because the API answered ` +
        `${error.code} (${error.status}), request ${error.requestId ?? "unknown"}`,
    );
  }

  return target;
}

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
 * The other half of the guard, for the session it cannot see.
 *
 * `guardRedirect` knows only that a cookie exists. Whether it is still worth
 * anything is the API's answer, and it arrives after the page has already been
 * asked to render — as `unauthenticated`. Left alone that lands on the
 * recoverable-error screen, which offers a retry; nothing about signing in
 * happens by asking the same question again, so the retry is a button that
 * cannot work. Send them to the form instead, carrying where they were going.
 *
 * `forbidden` is deliberately not included. That account is signed in and
 * simply not allowed, and the sign-in form would bounce it straight back.
 */
export function expiredSessionRedirect(error: unknown, pathname: string): string | null {
  if (!(error instanceof ApiError) || error.code !== "unauthenticated") return null;

  return `${SIGN_IN}?next=${encodeURIComponent(pathname)}`;
}

/**
 * Route protection, decided from the path and whether a session cookie exists.
 *
 * The cookie's presence is all this layer can know: it is httpOnly and only
 * the API can say whether it is still valid. That is deliberate. A cheap check
 * here keeps signed-out visitors off the constructor, and the API remains the
 * one that actually decides, answering `unauthenticated` if the cookie is
 * stale.
 */

/** Reachable without a session, because they are how a session is obtained. */
const PUBLIC_PATHS = ["/login"];

export function guardRedirect(pathname: string, hasSession: boolean): string | null {
  if (hasSession) return null;
  if (PUBLIC_PATHS.some((path) => pathname === path || pathname.startsWith(`${path}/`))) {
    return null;
  }

  // Where they were going is carried along, so signing in resumes the journey
  // instead of dropping them on a landing page.
  return `/login?next=${encodeURIComponent(pathname)}`;
}

/**
 * Route protection from the path and whether a session cookie exists. The
 * cookie is httpOnly, so only its presence is known here; the API decides
 * whether it is valid and answers `unauthenticated` when it is not.
 */

import { ApiError } from "@/lib/api/client";

/**
 * Reachable without a session, with every path beneath them. Beneath `/`
 * lies only `//…`, which `guardRedirect` refuses first.
 */
const PUBLIC_PATHS = ["/", "/login"];

/**
 * Reachable without a session, nothing beneath them. `/healthz` is the
 * container's liveness probe; redirected to sign-in, each probe would also
 * cost the API a settings read.
 */
const PUBLIC_EXACT = ["/healthz"];

/**
 * A contest's public table (docs/ARCHITECTURE.md §10). A whole-path pattern:
 * a `/contests/` prefix would open the play and staff screens.
 */
const PUBLIC_PATTERNS = [/^\/contests\/[^/]+\/leaderboard$/];

const SIGN_IN = "/login";

/** The one screen an account still on its one-time password may use. */
const PASSWORD_CHANGE = "/password";

/**
 * Where to send a request with no session, or null to let it through.
 * `pathname` decides and `search` is only carried. Matched with the query
 * attached, `/login?next=…` would stop matching sign-in and redirect to
 * itself in a loop, locking out every signed-out visitor.
 */
export function guardRedirect(
  pathname: string,
  search: string,
  hasSession: boolean,
): string | null {
  if (hasSession) return null;
  // "//my" would otherwise pass as "under /". The framework normalises it
  // today, but an access decision must not rest on that.
  if (pathname.startsWith("//")) {
    return `/login?next=${encodeURIComponent(pathname + search)}`;
  }
  if (PUBLIC_PATHS.some((path) => pathname === path || pathname.startsWith(`${path}/`))) {
    return null;
  }
  if (PUBLIC_EXACT.includes(pathname)) {
    return null;
  }
  if (PUBLIC_PATTERNS.some((pattern) => pattern.test(pathname))) {
    return null;
  }

  // The query is carried too: filters and searches live there.
  return `/login?next=${encodeURIComponent(pathname + search)}`;
}

/**
 * Where to send a page whose API read was refused for an auth reason, which
 * a retry cannot fix:
 *
 * - `unauthenticated`: back to sign-in, carrying the destination.
 * - `password_change_required`: to the password screen, the only one the API
 *   leaves open. Nothing is carried, since the change retires every session.
 *
 * `forbidden` is not included: the account is signed in, and sign-in would
 * bounce it back. Each redirect is logged with the API's request id, so an
 * unexpected sign-out can be matched to the server's log.
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

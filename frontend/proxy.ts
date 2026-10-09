import { NextResponse, type NextRequest } from "next/server";

import { ingressSecret } from "@/lib/api/config";
import { headedForApi, incomingRequestHeaders } from "@/lib/api/forwarded";
import { guardRedirect } from "@/lib/auth/guard";
import { SESSION_COOKIE } from "@/lib/auth/session";

/**
 * Next 16's `proxy` convention (formerly middleware), running on Node.
 *
 * First, on every path, it removes a forwarded client address the reverse proxy
 * did not vouch for (lib/api/forwarded.ts, CLAUDE.md rule 9): the API trusts
 * what this server sends, and the `/api/*` rewrite, which serves when no
 * reverse proxy is in front and matches in any letter case, passes browser
 * headers through unchanged. A rewrite cannot change headers, so this runs
 * first. Then it keeps signed-out visitors off screens that would
 * only fail at the API.
 */
export function proxy(request: NextRequest) {
  const towardsApi = headedForApi(request.nextUrl.pathname);
  const next = () =>
    NextResponse.next({
      request: { headers: incomingRequestHeaders(request.headers, ingressSecret(), { towardsApi }) },
    });

  // The API guards its own endpoints, public ones included.
  if (towardsApi) return next();

  // Path and query stay separate: joined, the guard would no longer recognise
  // `/login?next=…` and would loop.
  const target = guardRedirect(
    request.nextUrl.pathname,
    request.nextUrl.search,
    request.cookies.has(SESSION_COOKIE),
  );

  if (!target) return next();

  const url = request.nextUrl.clone();
  const [pathname, search = ""] = target.split("?");
  url.pathname = pathname;
  url.search = search;
  return NextResponse.redirect(url);
}

/**
 * Every route except the framework's assets. `/api/*` is matched only for
 * header cleanup: the reverse proxy (deploy/Caddyfile) or the rewrite sends it
 * to the API, which guards it (including public logos the sign-in page shows),
 * so `proxy` returns before the guard for that prefix in any letter case.
 *
 * Exclusions are named rather than "anything with a dot", which would silently
 * stop guarding a route that legitimately contains one.
 */
export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon\\.ico|robots\\.txt|sitemap\\.xml).*)"],
};

import { NextResponse, type NextRequest } from "next/server";

import { ingressSecret } from "@/lib/api/config";
import { headedForApi, incomingRequestHeaders } from "@/lib/api/forwarded";
import { guardRedirect } from "@/lib/auth/guard";
import { sessionCookie } from "@/lib/auth/session";
import { contentSecurityPolicy, NONCE_REQUEST_HEADERS, newNonce, STATIC_CONTENT_SECURITY_POLICY } from "@/lib/csp";

/**
 * Next 16's `proxy` convention (formerly middleware), running on Node.
 *
 * First, on every path, it removes a forwarded client address the reverse proxy
 * did not vouch for (lib/api/forwarded.ts, CLAUDE.md rule 9): the API trusts
 * what this server sends, and the `/api/*` rewrite, which serves when no
 * reverse proxy is in front and matches in any letter case, passes browser
 * headers through unchanged. A rewrite cannot change headers, so this runs
 * first. It removes a content security policy the browser sent too, since Next
 * would stamp the page with that policy's nonce. Then it keeps signed-out
 * visitors off screens that would only fail at the API, and serves every other
 * screen under a policy with a fresh nonce (lib/csp.ts). An `/api/*` path gets
 * the static policy here, and a path its matcher skips gets it from
 * next.config.ts.
 */
export function proxy(request: NextRequest) {
  const towardsApi = headedForApi(request.nextUrl.pathname);
  const headers = incomingRequestHeaders(request.headers, ingressSecret(), { towardsApi });
  for (const name of NONCE_REQUEST_HEADERS) headers.delete(name);

  // The API guards its own endpoints, public ones included. With no API to
  // rewrite to, Next renders its not-found page here, with no nonce.
  if (towardsApi) {
    const response = NextResponse.next({ request: { headers } });
    if (enforcesPolicy()) response.headers.set("content-security-policy", STATIC_CONTENT_SECURITY_POLICY);
    return response;
  }

  // Path and query stay separate: joined, the guard would no longer recognise
  // `/login?next=…` and would loop.
  const target = guardRedirect(
    request.nextUrl.pathname,
    request.nextUrl.search,
    request.cookies.has(sessionCookie().name),
  );

  if (!target) return screen(headers);

  const url = request.nextUrl.clone();
  const [pathname, search = ""] = target.split("?");
  url.pathname = pathname;
  url.search = search;
  return NextResponse.redirect(url);
}

/**
 * Production only: React's development stack traces use `eval` and hot reload
 * a WebSocket, and loosening the real policy for them is how `'unsafe-eval'`
 * gets shipped.
 */
function enforcesPolicy(): boolean {
  return process.env.NODE_ENV === "production";
}

/**
 * The browser enforces the policy the response carries and Next reads the
 * nonce from the request's, so both carry the same value.
 */
function screen(headers: Headers) {
  if (!enforcesPolicy()) return NextResponse.next({ request: { headers } });

  const policy = contentSecurityPolicy(newNonce());
  headers.set("content-security-policy", policy);
  const response = NextResponse.next({ request: { headers } });
  response.headers.set("content-security-policy", policy);
  return response;
}

/**
 * Every route except the framework's assets and the public brand marks. `/api/*` is matched only for
 * header cleanup: the reverse proxy (deploy/Caddyfile) or the rewrite sends it
 * to the API, which guards it (including public logos the sign-in page shows),
 * so `proxy` returns before the guard for that prefix in any letter case.
 *
 * Exclusions are named rather than "anything with a dot", which would silently
 * stop guarding a route that legitimately contains one. They are
 * `UNPROXIED_PATHS` in lib/csp.ts, written out because Next reads only a
 * literal; next.config.test.ts holds the two together.
 */
export const config = {
  matcher: [
    "/((?!_next/static|_next/image|favicon\\.ico|icon\\.svg|apple-icon\\.png|robots\\.txt|sitemap\\.xml|brand/).*)",
  ],
};

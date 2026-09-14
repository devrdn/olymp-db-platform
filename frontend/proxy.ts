import { NextResponse, type NextRequest } from "next/server";

import { ingressSecret } from "@/lib/api/config";
import { headedForApi, incomingRequestHeaders } from "@/lib/api/forwarded";
import { guardRedirect } from "@/lib/auth/guard";
import { SESSION_COOKIE } from "@/lib/auth/session";

/**
 * Named `proxy` rather than `middleware`: Next 16 renamed the convention to
 * make the network boundary explicit, and the runtime is Node with no edge
 * option.
 *
 * Two jobs.
 *
 * On every request, whatever the path: remove a forwarded client address the
 * reverse proxy did not vouch for (lib/api/forwarded.ts). The API believes the
 * address this server sends, and the `/api/*` rewrite in next.config.ts passes
 * the browser's headers to it as they came — only when no reverse proxy is in
 * front, and matching the prefix without regard to case. A rewrite cannot
 * change headers; this runs before it, and does not depend on spelling.
 *
 * Then keep a signed-out visitor off a screen that would only fail at the API.
 * Language is not decided here, because it is not in the URL.
 */
export function proxy(request: NextRequest) {
  const towardsApi = headedForApi(request.nextUrl.pathname);
  const next = () =>
    NextResponse.next({
      request: { headers: incomingRequestHeaders(request.headers, ingressSecret(), { towardsApi }) },
    });

  // The API guards its own endpoints, the public ones among them.
  if (towardsApi) return next();

  // Path and query are passed separately: the path decides whether the screen
  // is public, the query is only carried along so that a filtered register or
  // a search somebody typed survives signing in. Joined into one string, the
  // guard would stop recognising its own `/login?next=…` and loop.
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
 * Every route except the framework's own assets.
 *
 * `/api/*` is included, but only for its headers. It is not a route this
 * application serves: the reverse proxy sends it to the API before Next is
 * reached (deploy/Caddyfile), and without one the rewrite does — in any letter
 * case. Guarding it
 * would mean answering a sign-in redirect on behalf of endpoints that are the
 * API's to guard, including the ones that are deliberately public — the logo
 * and the icons the sign-in screen itself wears, which are requested by a
 * browser that has, by definition, not signed in yet — so `proxy` returns
 * before the guard for that prefix, in any case.
 *
 * The exclusions are named rather than inferred from the path. Skipping
 * anything containing a dot is the usual shorthand and it is a hole with a
 * timer on it: the day a route legitimately carries one — a file name, a
 * version, an identifier that is not a UUID — the guard stops running on it
 * and nothing says so.
 */
export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon\\.ico|robots\\.txt|sitemap\\.xml).*)"],
};

import { NextResponse, type NextRequest } from "next/server";

import { guardRedirect } from "@/lib/auth/guard";
import { SESSION_COOKIE } from "@/lib/auth/session";

/**
 * Named `proxy` rather than `middleware`: Next 16 renamed the convention to
 * make the network boundary explicit, and the runtime is Node with no edge
 * option.
 *
 * One job: keep a signed-out visitor off a screen that would only fail at the
 * API. Language is not decided here, because it is not in the URL.
 */
export function proxy(request: NextRequest) {
  // Path *and* query. A filtered register, a page of results, a search
  // somebody typed all live in the query string, and carrying only the path
  // lands them afterwards on a bare list wondering where their search went.
  const target = guardRedirect(
    request.nextUrl.pathname + request.nextUrl.search,
    request.cookies.has(SESSION_COOKIE),
  );

  if (!target) return NextResponse.next();

  const url = request.nextUrl.clone();
  const [pathname, search = ""] = target.split("?");
  url.pathname = pathname;
  url.search = search;
  return NextResponse.redirect(url);
}

/**
 * Every route except the framework's own assets.
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

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
  const target = guardRedirect(
    request.nextUrl.pathname,
    request.cookies.has(SESSION_COOKIE),
  );

  if (!target) return NextResponse.next();

  const url = request.nextUrl.clone();
  const [pathname, search = ""] = target.split("?");
  url.pathname = pathname;
  url.search = search;
  return NextResponse.redirect(url);
}

export const config = {
  // Everything except Next's own assets and files with an extension.
  matcher: ["/((?!_next|.*\\..*).*)"],
};

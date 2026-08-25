import { NextResponse, type NextRequest } from "next/server";

import { localeRedirect } from "@/lib/i18n/routing";

/**
 * Named `proxy` rather than `middleware`: Next 16 renamed the convention to
 * make the network boundary explicit, and the runtime is Node with no edge
 * option.
 *
 * Its only job is language. A request without a locale prefix is negotiated
 * from the browser's own preferences and redirected once, so every page below
 * can assume the segment is there.
 */
export function proxy(request: NextRequest) {
  const target = localeRedirect(
    request.nextUrl.pathname,
    request.headers.get("accept-language"),
  );

  if (!target) return NextResponse.next();

  const url = request.nextUrl.clone();
  url.pathname = target;
  return NextResponse.redirect(url);
}

export const config = {
  // Everything except Next's own assets and files with an extension.
  matcher: ["/((?!_next|.*\\..*).*)"],
};

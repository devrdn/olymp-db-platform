import type { ParsedCookie } from "./cookie";

/**
 * Whether the session cookie is marked `Secure`, from `COOKIE_SECURE` as on
 * the Go side. TLS is a property of the deployment, not of NODE_ENV: a
 * production build served over plain http (`make front-start`) would have its
 * Secure cookie silently discarded. Never inferred from a forwarded header,
 * since a wrong guess would drop the attribute in production. Unset defaults
 * to Secure in production builds.
 */
export function cookieSecure(): boolean {
  const configured = process.env.COOKIE_SECURE;
  if (!configured) return process.env.NODE_ENV === "production";

  // The spellings Go's strconv.ParseBool accepts: the cookie's name follows
  // this value, and the API names it by that rule.
  if (["1", "t", "T", "TRUE", "true", "True"].includes(configured)) return true;
  if (["0", "f", "F", "FALSE", "false", "False"].includes(configured)) return false;

  // The API refuses to start on such a value; guessing here would pick a
  // cookie name it never sets.
  throw new Error(`COOKIE_SECURE is ${JSON.stringify(configured)}: set it to true or false`);
}

/**
 * The attributes a cookie the API issued is handed to the browser with. The
 * name, value, lifetime and path are the API's (a lifetime of zero clears
 * it); HttpOnly, SameSite and Secure are this deployment's, as the API's
 * own cookie writer sets them.
 */
export function browserCookieOptions(cookie: ParsedCookie) {
  return {
    path: cookie.path ?? "/",
    maxAge: cookie.maxAge,
    httpOnly: true,
    sameSite: "lax" as const,
    secure: cookieSecure(),
  };
}

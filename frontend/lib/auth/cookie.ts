/**
 * Parses the API's `Set-Cookie`, which reaches the server during sign-in and
 * must be handed on. Max-Age is kept, or the cookie would die with the
 * browser. HttpOnly, SameSite and Secure are not read: they are set again from
 * this application's configuration (see cookie-policy.ts).
 */

export type ParsedCookie = { name: string; value: string; maxAge?: number; path?: string };

/** A header that opens with an attribute name carries no cookie. */
const ATTRIBUTE_NAMES = new Set([
  "path",
  "domain",
  "expires",
  "max-age",
  "secure",
  "httponly",
  "samesite",
  "priority",
  "partitioned",
]);

export function parseSetCookie(header: string): ParsedCookie | null {
  const [pair, ...attributes] = header.split(";").map((part) => part.trim());

  const separator = pair.indexOf("=");
  if (separator <= 0) return null;
  if (ATTRIBUTE_NAMES.has(pair.slice(0, separator).trim().toLowerCase())) return null;

  const parsed: ParsedCookie = {
    name: pair.slice(0, separator),
    value: pair.slice(separator + 1),
  };

  for (const attribute of attributes) {
    const [key, value = ""] = attribute.split("=");
    const name = key.trim().toLowerCase();

    if (name === "max-age") {
      const seconds = Number(value.trim());
      if (!Number.isNaN(seconds)) parsed.maxAge = seconds;
    } else if (name === "path") {
      parsed.path = value.trim();
    }
  }

  return parsed;
}

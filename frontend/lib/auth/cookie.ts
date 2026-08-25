/**
 * Reading the session cookie the API issued.
 *
 * Sign-in happens in a Server Action, so the API's `Set-Cookie` arrives at the
 * server rather than at the browser. It has to be handed on deliberately, and
 * its lifetime carried with it: the server decides how long a session lives,
 * and dropping Max-Age here would quietly turn it into a session cookie that
 * dies when the browser closes.
 *
 * HttpOnly, SameSite and Secure are not read back. They are set again on the
 * outgoing cookie from this application's own configuration, because a browser
 * over plain HTTP silently discards a Secure cookie and a local stack has no
 * certificate.
 */

export type ParsedCookie = { name: string; value: string; maxAge?: number; path?: string };

/**
 * A Set-Cookie header always opens with the pair. If the first thing is an
 * attribute name, there is no cookie here and "Path" is not its name.
 */
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

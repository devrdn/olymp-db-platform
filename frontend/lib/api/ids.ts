/**
 * The check that stands between a submitted identifier and a request path.
 *
 * Every identifier the API issues is a UUID, and every one the interface sends
 * back arrives from somewhere a visitor controls — a form field, a route
 * segment, a query string. Interpolating that into a path without checking it
 * is how `/contests/${id}/enroll` becomes a request to `/auth/logout`: `fetch`
 * resolves `..` against the base before the request is made, so the segment
 * that steered it is gone by the time anything downstream could notice.
 *
 * A route segment is checked for the same reason, one step earlier: a page
 * that passes a malformed segment through spends a request to be told 400,
 * where the honest answer is that no such address exists.
 *
 * Anchored, and with no `\n` allowance — an unanchored pattern matches a valid
 * UUID sitting inside a hostile string, and in JavaScript `$` matches before a
 * trailing newline, which is exactly the character a header injection needs.
 */
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isId(value: unknown): value is string {
  return typeof value === "string" && UUID.test(value);
}

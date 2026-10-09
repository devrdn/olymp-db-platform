// Anchored: an unanchored pattern accepts a UUID inside a hostile string.
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * Whether `value` is an identifier safe to put into a request path. Every id
 * the API issues is a UUID, but the one sent back comes from a visitor;
 * unchecked, `/contests/${id}/enroll` can become `/auth/logout`, since
 * `fetch` resolves `..` before sending.
 */
export function isId(value: unknown): value is string {
  return typeof value === "string" && UUID.test(value);
}

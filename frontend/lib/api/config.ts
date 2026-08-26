/**
 * Where the Core API lives, from the server's point of view.
 *
 * Never from the browser's: in production the interface and the API share one
 * origin behind the reverse proxy, so a request from the page needs no origin
 * at all. This is the address the Next server dials, which is a different thing
 * and lives on a private network.
 */

/** The address a development stack runs the API on, and nowhere else. */
const LOCAL_STACK = "http://localhost:8080";

export function apiOrigin(): string {
  const configured = process.env.API_ORIGIN;
  if (configured) return configured;

  /**
   * A missing address in production is a broken deployment, and guessing at it
   * turns that into a page that says the server is unreachable — true, and
   * useless, because nothing names the cause. The same rule the architecture
   * applies to its own optional dependencies (section 3.1): the substitution
   * is loud or it does not happen.
   */
  if (process.env.NODE_ENV === "production") {
    throw new Error(
      "API_ORIGIN is not set. It is required in production: the interface has no other way to reach the Core API.",
    );
  }

  return LOCAL_STACK;
}

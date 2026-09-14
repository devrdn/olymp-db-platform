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

/** Shorter than this is no secret at all (lib/api/forwarded.ts). */
const MIN_INGRESS_SECRET_LENGTH = 32;

let reportedMissingIngressSecret = false;

/**
 * The secret the reverse proxy adds to every request it forwards here
 * (`INGRESS_SECRET`, deploy/Caddyfile), or null when none usable is set.
 *
 * Null is safe — no forwarded address is handed to the API, so nobody can
 * choose theirs — but behind the proxy it makes every visitor look like this
 * server, and the per-address login limit becomes one counter for everybody.
 * In production that is almost always a deployment that forgot the variable,
 * so it is reported once per process, naming the variable and not the value.
 * A production build served locally without a proxy (`make front-start`) sees
 * the same line once, and can ignore it.
 */
export function ingressSecret(): string | null {
  const configured = process.env.INGRESS_SECRET ?? "";
  if (configured.length >= MIN_INGRESS_SECRET_LENGTH) return configured;

  if (process.env.NODE_ENV === "production" && !reportedMissingIngressSecret) {
    reportedMissingIngressSecret = true;
    console.error(
      `INGRESS_SECRET is ${configured ? "shorter than " + MIN_INGRESS_SECRET_LENGTH + " characters" : "not set"}: ` +
        "client addresses are not forwarded to the API, so every request counts as coming from this server. " +
        "Set the same value for the caddy and web services.",
    );
  }
  return null;
}

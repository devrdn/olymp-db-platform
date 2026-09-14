import { ingressSecretProblem } from "./ingress-secret.mjs";

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

let reportedMissingIngressSecret = false;

/**
 * The secret the reverse proxy adds to every request it forwards here
 * (`INGRESS_SECRET`, deploy/Caddyfile), or null when none usable is set.
 *
 * A production server does not start without a usable one (scripts/start.mjs),
 * so null there means a production build served locally with the explicit
 * flag, or a server started some other way. Null is safe — no forwarded address
 * is handed to the API, so nobody can choose theirs — but behind the proxy it
 * would make every visitor look like this server, so in production it is
 * reported once per process, naming the variable and not the value.
 */
export function ingressSecret(): string | null {
  const configured = process.env.INGRESS_SECRET;
  const production = process.env.NODE_ENV === "production";
  const problem = ingressSecretProblem(configured, production);
  if (!problem) return configured ?? null;

  if (production && !reportedMissingIngressSecret) {
    reportedMissingIngressSecret = true;
    console.error(
      `INGRESS_SECRET is ${problem}: ` +
        "client addresses are not forwarded to the API, so every request counts as coming from this server. " +
        "Set the same generated value for the caddy and web services.",
    );
  }
  return null;
}

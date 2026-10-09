import { ingressSecretProblem } from "./ingress-secret.mjs";

/**
 * Server-side configuration: where the Next server reaches the Core API on the
 * private network, and the ingress secret. The browser needs no origin, since
 * the page and the API share one behind the reverse proxy.
 */

/** Development only. */
const LOCAL_STACK = "http://localhost:8080";

export function apiOrigin(): string {
  const configured = process.env.API_ORIGIN;
  if (configured) return configured;

  // In production a guess would hide a broken deployment behind "unreachable"
  // (docs/ARCHITECTURE.md §3.1: a substitution is loud or it does not happen).
  if (process.env.NODE_ENV === "production") {
    throw new Error(
      "API_ORIGIN is not set. It is required in production: the interface has no other way to reach the Core API.",
    );
  }

  return LOCAL_STACK;
}

let reportedMissingIngressSecret = false;

/**
 * The secret the reverse proxy adds to every request (`INGRESS_SECRET`,
 * deploy/Caddyfile), or null when none usable is set. Null is safe (no address
 * is forwarded) but makes every visitor look like this server, so production
 * reports it once per process, naming the variable and not the value.
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

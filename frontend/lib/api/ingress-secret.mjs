/**
 * What makes INGRESS_SECRET usable, in one place for the two things that ask:
 * the server's entry point (scripts/start.mjs), which refuses to start a
 * production server without a usable secret, and `ingressSecret()` in
 * ./config.ts, which reads it for every request.
 *
 * Plain JavaScript rather than TypeScript because the entry point runs under
 * bare Node in the container, before Next and outside its build: the file is
 * copied next to server.js as it is (see the Dockerfile).
 *
 * See lib/api/forwarded.ts for what the secret proves and why the interface
 * forwards no client address without it.
 */

/** Shorter than this is no secret at all: the 256-bit floor of the backend's secrets. */
export const MIN_INGRESS_SECRET_LENGTH = 32;

/** How deploy/.env.example marks a value an operator must choose. */
const PLACEHOLDER_MARKER = "change-me";

/**
 * The flag that lets a production build start without a secret. Only for
 * serving that build locally with no reverse proxy in front (`make
 * front-start`, `npm run smoke`), where there is nothing for a secret to prove.
 * It must be exactly "true".
 */
export const ALLOW_MISSING_FLAG = "ALLOW_MISSING_INGRESS_SECRET";

/**
 * Why a configured secret is unusable, or null when it is usable. The reason
 * never repeats the value. The example file's placeholder is refused only in
 * production: a development stack may keep the example's values.
 *
 * @param {string | undefined} value
 * @param {boolean} production
 * @returns {string | null}
 */
export function ingressSecretProblem(value, production) {
  if (!value) return "not set";
  if (production && value.toLowerCase().includes(PLACEHOLDER_MARKER)) {
    return "still the placeholder from deploy/.env.example";
  }
  if (value.length < MIN_INGRESS_SECRET_LENGTH) {
    return `shorter than ${MIN_INGRESS_SECRET_LENGTH} characters`;
  }
  return null;
}

/**
 * The message a server should refuse to start with, or null when it may start.
 * Only a production server refuses, and not when {@link ALLOW_MISSING_FLAG} is
 * "true".
 *
 * @param {Record<string, string | undefined>} env
 * @returns {string | null}
 */
export function startupRefusal(env) {
  if (env.NODE_ENV !== "production" || env[ALLOW_MISSING_FLAG] === "true") return null;
  const problem = ingressSecretProblem(env.INGRESS_SECRET, true);
  if (!problem) return null;
  return (
    `INGRESS_SECRET is ${problem}. Behind Caddy it proves a request's client address, and without it ` +
    "every visitor would count as this server. Set the same generated value (openssl rand -hex 32) for " +
    `the caddy and web services, or set ${ALLOW_MISSING_FLAG}=true to serve a production build locally ` +
    "without a proxy."
  );
}

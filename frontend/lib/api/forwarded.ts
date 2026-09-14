import { createHash, timingSafeEqual } from "node:crypto";

import { MIN_INGRESS_SECRET_LENGTH } from "./ingress-secret.mjs";

/**
 * The headers that carry who is really asking, from the browser's request to
 * this server's own request against the API.
 *
 * Every request the Go API sees comes from this server: sign-in, enrolment
 * and every admin action are Server Actions that dial the API themselves.
 * The API resolves the client address from `X-Forwarded-For` — believing it
 * only from its configured proxies, of which this server is one — so if the
 * address the ingress proxy established is not handed on here, the API's
 * answer to "who?" is this process. Three real things then break: the
 * per-address login throttle collapses into one counter for the whole
 * installation, a contest's network restriction compares against the web
 * container instead of the participant, and the audit trail records the proxy
 * for every action.
 *
 * The other half of that trust is this server's to keep. The API believes what
 * this server hands on, so this server may hand on only an address the ingress
 * proxy set. Next does not expose the socket a request arrived on, so "it came
 * through the proxy" cannot be read off the connection; the proxy proves it
 * instead, with a secret it adds to every request it forwards here
 * (`INGRESS_SECRET`, sent as {@link INGRESS_HEADER}, see deploy/Caddyfile). A
 * request without that secret reached this server some other way, and its
 * `X-Forwarded-For` is whatever the browser chose to write.
 */

/** The header the ingress proxy proves itself with. Lower case, as Node reads it. */
export const INGRESS_HEADER = "x-ingress-secret";

/**
 * Whether the presented value is the configured ingress secret.
 *
 * Both sides are hashed before the comparison, so it is constant-time whatever
 * the presented length. An absent or too-short configured secret vouches for
 * nothing: an unconfigured server cannot tell the proxy from a browser, so it
 * believes neither.
 */
export function vouchedByIngress(
  presented: string | null | undefined,
  configured: string | null | undefined,
): boolean {
  if (!configured || configured.length < MIN_INGRESS_SECRET_LENGTH) return false;
  if (!presented) return false;

  const digest = (value: string) => createHash("sha256").update(value, "utf8").digest();
  return timingSafeEqual(digest(presented), digest(configured));
}

/**
 * A forwarded chain is addresses joined by commas. The exact parsing belongs
 * to the API, which walks it right to left past its trusted proxies; this
 * only refuses values that could not be an address chain at all, because the
 * value goes into an outgoing header and `fetch` answers a control character
 * by throwing — hostile input must not turn into a failed sign-in.
 */
const plausibleChain = /^[0-9a-fA-F.:,\s]+$/;

/**
 * Builds the pass-through headers from a reader over the incoming request's
 * own. Injected as functions and values so the logic is testable without a
 * framework; the wiring hands it `headers().get` from `next/headers` and the
 * configured secret.
 */
export function forwardedHeaders(
  get: (name: string) => string | null,
  secret: string | null | undefined,
): Record<string, string> {
  const headers: Record<string, string> = {};

  // Nothing is handed on unless the proxy vouched for this request: an
  // unvouched chain is the browser's own claim about its address.
  if (!vouchedByIngress(get(INGRESS_HEADER), secret)) return headers;

  const chain = get("x-forwarded-for");
  if (chain && plausibleChain.test(chain)) {
    headers["x-forwarded-for"] = chain;
  }

  // Only alongside an address: a request with no chain never crossed the
  // proxy, and its user agent is whatever dialled this server directly.
  const agent = get("user-agent");
  if (headers["x-forwarded-for"] && agent) {
    // The audit column is bounded server-side; the header just has to be a
    // legal value. Control characters would make fetch throw.
    headers["user-agent"] = agent.replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 512);
  }

  return headers;
}

/**
 * Headers that name a client address. The API reads only `X-Forwarded-For`
 * (backend CLAUDE.md, rule 9), but none of these may reach it — or anything
 * else — as a claim nobody vouched for.
 */
const FORWARDED_ADDRESS_HEADERS = ["x-forwarded-for", "x-real-ip", "forwarded"];

/**
 * Whether a path is headed for the API through the `/api/*` rewrite in
 * next.config.ts. Case-insensitive, because the rewrite's own match is: a
 * lower-case check alone is how `/API/...` was passed through untouched.
 */
export function headedForApi(pathname: string): boolean {
  return pathname.toLowerCase().startsWith("/api/");
}

/**
 * The headers every request continues with, from the proxy file (proxy.ts).
 *
 * A forwarded address the ingress proxy did not vouch for is removed whatever
 * the path: the `/api/*` rewrite passes the browser's headers to the API as
 * they came, and a rule that depends on recognising that path is a rule a
 * differently-spelled path walks past. The proxy's secret is removed on the way
 * to the API, which has no use for it; on a screen it is kept, because
 * {@link forwardedHeaders} checks it again at the point it hands an address on.
 * Returns a copy; the incoming headers are left as they were.
 */
export function incomingRequestHeaders(
  incoming: Headers,
  secret: string | null | undefined,
  { towardsApi }: { towardsApi: boolean },
): Headers {
  const outgoing = new Headers(incoming);
  if (!vouchedByIngress(incoming.get(INGRESS_HEADER), secret)) {
    for (const name of FORWARDED_ADDRESS_HEADERS) outgoing.delete(name);
  }
  if (towardsApi) outgoing.delete(INGRESS_HEADER);
  return outgoing;
}

import { createHash, timingSafeEqual } from "node:crypto";

import { MIN_INGRESS_SECRET_LENGTH } from "./ingress-secret.mjs";

/**
 * Hands the real client address on from the browser's request to this
 * server's own requests against the API.
 *
 * Every request the API sees comes from this server (Server Actions), and the
 * API trusts this server's `X-Forwarded-For`. Without it, the login throttle,
 * a contest's network restriction and the audit trail would all see this
 * process. In turn, this server may hand on only an address the ingress proxy
 * set. Next does not expose the socket, so the proxy proves itself with a
 * secret (`INGRESS_SECRET`, sent as {@link INGRESS_HEADER}, see
 * deploy/Caddyfile); without it, `X-Forwarded-For` is the browser's own claim.
 */

/** Lower case, as Node reads it. */
export const INGRESS_HEADER = "x-ingress-secret";

/**
 * Whether the presented value is the configured ingress secret, compared in
 * constant time over hashes. An absent or too-short configured secret vouches
 * for nothing.
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
 * Parsing the chain is the API's job; this only refuses values that cannot be
 * an address chain, since `fetch` throws on a control character in a header
 * and hostile input must not become a failed sign-in.
 */
const plausibleChain = /^[0-9a-fA-F.:,\s]+$/;

/** Builds the pass-through headers; `get` is `headers().get` from `next/headers`. */
export function forwardedHeaders(
  get: (name: string) => string | null,
  secret: string | null | undefined,
): Record<string, string> {
  const headers: Record<string, string> = {};

  if (!vouchedByIngress(get(INGRESS_HEADER), secret)) return headers;

  const chain = get("x-forwarded-for");
  if (chain && plausibleChain.test(chain)) {
    headers["x-forwarded-for"] = chain;
  }

  // Only alongside an address: a request with no forwarded chain is not a
  // browser's, so its user agent says nothing about a person.
  const agent = get("user-agent");
  if (headers["x-forwarded-for"] && agent) {
    // Control characters would make fetch throw.
    headers["user-agent"] = agent.replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 512);
  }

  return headers;
}

/**
 * The API reads only `X-Forwarded-For` (CLAUDE.md rule 9), but none of these
 * may pass on unvouched.
 */
const FORWARDED_ADDRESS_HEADERS = ["x-forwarded-for", "x-real-ip", "forwarded"];

/**
 * Whether a path goes to the API through the `/api/*` rewrite. Case-insensitive,
 * because the rewrite's match is.
 */
export function headedForApi(pathname: string): boolean {
  return pathname.toLowerCase().startsWith("/api/");
}

/**
 * A copy of the headers every request continues with, from proxy.ts. An
 * unvouched forwarded address is removed whatever the path, so a
 * differently-spelled path cannot slip past. The secret is removed towards the
 * API but kept for a screen, where {@link forwardedHeaders} checks it again.
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

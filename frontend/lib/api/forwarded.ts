/**
 * The headers that carry who is really asking, from the browser's request to
 * this server's own request against the API.
 *
 * Every request the Go API sees comes from this server: sign-in, enrolment
 * and every admin action are Server Actions that dial the API themselves.
 * The API resolves the client address from `X-Forwarded-For` — believing it
 * only from its configured proxies, of which this server is one — so if the
 * chain the ingress proxy established is not handed on here, the API's answer
 * to "who?" is this process. Three real things then break: the per-address
 * login throttle collapses into one counter for the whole installation, a
 * contest's network restriction compares against the web container instead of
 * the participant, and the audit trail records the proxy for every action.
 */

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
 * own. Injected as a function so the logic is testable without a framework;
 * the wiring hands it `headers().get` from `next/headers`.
 */
export function forwardedHeaders(
  get: (name: string) => string | null,
): Record<string, string> {
  const headers: Record<string, string> = {};

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

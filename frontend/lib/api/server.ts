import { apiOrigin } from "./config";
import { callerHeaders } from "./caller";
import { request, type RequestOptions } from "./client";
import { sessionHeader } from "@/lib/auth/session";

/**
 * How long a server-side call may wait for the API. Longer than the longest
 * answer the API itself waits for (a query queued for `QUERY_QUEUE_WAIT`,
 * 15 s, then run for its 5 s deadline), so a slow answer is never cut short;
 * without any bound, Node's fetch holds a request open for minutes, and a
 * stalled API holds every page's render with it.
 */
export const SERVER_REQUEST_TIMEOUT_MS = 30_000;

/**
 * Server-side access to the Core API: the transport plus where the API is and
 * who is asking, both the session (which account) and the forwarded chain
 * (which address; see `./forwarded`). A caller's own signal replaces the
 * deadline; a timed-out one surfaces as the `unreachable` ApiError.
 */
export async function serverRequest(path: string, options: RequestOptions = {}) {
  return request(path, {
    ...options,
    origin: apiOrigin(),
    headers: { ...options.headers, ...(await callerHeaders()), ...(await sessionHeader()) },
    signal: options.signal ?? AbortSignal.timeout(SERVER_REQUEST_TIMEOUT_MS),
  });
}

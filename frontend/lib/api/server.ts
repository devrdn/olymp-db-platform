import { apiOrigin } from "./config";
import { callerHeaders } from "./caller";
import { request, type RequestOptions } from "./client";
import { sessionHeader } from "@/lib/auth/session";

/**
 * Server-side access to the Core API: the transport plus where the API is and
 * who is asking, both the session (which account) and the forwarded chain
 * (which address; see `./forwarded`).
 */
export async function serverRequest(path: string, options: RequestOptions = {}) {
  return request(path, {
    ...options,
    origin: apiOrigin(),
    headers: { ...options.headers, ...(await callerHeaders()), ...(await sessionHeader()) },
  });
}

import { apiOrigin } from "./config";
import { request, type RequestOptions } from "./client";
import { sessionHeader } from "@/lib/auth/session";

/**
 * Server-side access to the Core API: the transport, plus the two things only
 * the server knows — where the API is, and who is asking.
 *
 * Neither is spelled out here. The address comes from `./config`, which refuses
 * to guess in production, and the session from `lib/auth`, which owns the
 * cookie's name. A second copy of either in this file is how the two drift.
 */
export async function serverRequest(path: string, options: RequestOptions = {}) {
  return request(path, {
    ...options,
    origin: apiOrigin(),
    headers: { ...options.headers, ...(await sessionHeader()) },
  });
}

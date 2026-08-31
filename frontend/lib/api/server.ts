import { apiOrigin } from "./config";
import { callerHeaders } from "./caller";
import { request, type RequestOptions } from "./client";
import { sessionHeader } from "@/lib/auth/session";

/**
 * Server-side access to the Core API: the transport, plus the two things only
 * the server knows — where the API is, and who is asking.
 *
 * "Who is asking" is two headers, not one. The session says which account;
 * the forwarded chain says from which address — and since every request the
 * API sees leaves from this server, dropping the chain would make the API
 * throttle, audit and IP-restrict this process instead of the person.
 *
 * Neither is spelled out here. The address comes from `./config`, which refuses
 * to guess in production, and the session from `lib/auth`, which owns the
 * cookie's name. A second copy of either in this file is how the two drift.
 */
export async function serverRequest(path: string, options: RequestOptions = {}) {
  return request(path, {
    ...options,
    origin: apiOrigin(),
    headers: { ...options.headers, ...(await callerHeaders()), ...(await sessionHeader()) },
  });
}

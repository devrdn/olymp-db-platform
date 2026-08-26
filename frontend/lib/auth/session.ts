import { cookies } from "next/headers";

import { API_PREFIX } from "@/lib/api/client";
import { apiOrigin } from "@/lib/api/config";

/**
 * The session cookie's name, written here and nowhere else.
 *
 * It has to match `auth.SessionCookieName` in the Go service. One constant on
 * each side of the wire is unavoidable; two on this side is how they drift.
 */
export const SESSION_COOKIE = "dbcontest_session";

export type CurrentIdentity = { id: string; login: string; permissions: string[] };

/**
 * The session, shaped as a header for a server-to-server call.
 *
 * A Server Component's `fetch` carries no cookies of its own, so the session
 * has to be attached by hand. Callers get an object they can spread, empty when
 * there is nothing to send, which keeps the decision here rather than repeated
 * at every call site.
 */
export async function sessionHeader(): Promise<Record<string, string>> {
  const jar = await cookies();
  const session = jar.get(SESSION_COOKIE)?.value;

  return session ? { cookie: `${SESSION_COOKIE}=${session}` } : {};
}

/**
 * Who the caller is, according to the API.
 *
 * `/auth/me` reports permissions rather than roles, so the interface branches
 * on the same thing the server's middleware does and a role added as data
 * needs no change here. Returns null when there is no usable session, which is
 * what route protection reads.
 */
export async function fetchIdentity(): Promise<CurrentIdentity | null> {
  const header = await sessionHeader();
  if (!header.cookie) return null;

  const response = await fetch(`${apiOrigin()}${API_PREFIX}/auth/me`, {
    headers: header,
    cache: "no-store",
  }).catch(() => null);

  if (!response?.ok) return null;

  return (await response.json()) as CurrentIdentity;
}

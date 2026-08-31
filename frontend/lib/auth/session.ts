import { cookies } from "next/headers";

import { callerHeaders } from "@/lib/api/caller";
import { API_PREFIX } from "@/lib/api/client";
import { apiOrigin } from "@/lib/api/config";

/**
 * The session cookie's name, written here and nowhere else.
 *
 * It has to match `auth.SessionCookieName` in the Go service. One constant on
 * each side of the wire is unavoidable; two on this side is how they drift.
 */
export const SESSION_COOKIE = "dbcontest_session";

/**
 * Who the caller is, as `/auth/me` reports it.
 *
 * Permissions are what route protection branches on — the same thing the
 * server's middleware decides on, so a role added as data needs no change
 * here. The name and roles are what a profile shows: a person is greeted by
 * name, and told what they are, which a permission list spells out but does
 * not name.
 */
export type CurrentIdentity = {
  id: string;
  login: string;
  fullName: string;
  email?: string;
  roles: string[];
  permissions: string[];
};

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
    headers: { ...(await callerHeaders()), ...header },
    cache: "no-store",
  }).catch(() => null);

  if (!response?.ok) return null;

  const body = (await response.json()) as {
    id: string;
    login: string;
    full_name?: string;
    email?: string;
    roles?: string[];
    permissions?: string[];
  };

  return {
    id: body.id,
    login: body.login,
    // The API leaves the name out when it could not read the account behind
    // the session — it does not fail the request over a display field. The
    // login is always there, and is what the interface falls back to.
    fullName: body.full_name ?? "",
    email: body.email,
    roles: body.roles ?? [],
    permissions: body.permissions ?? [],
  };
}

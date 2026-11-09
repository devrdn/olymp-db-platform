import { cookies } from "next/headers";
import { cache } from "react";

import { callerHeaders } from "@/lib/api/caller";
import { API_PREFIX } from "@/lib/api/client";
import { apiOrigin } from "@/lib/api/config";
import { SERVER_REQUEST_TIMEOUT_MS } from "@/lib/api/server";

import { cookieSecure } from "./cookie-policy";

/**
 * The session cookie's name and the attributes that must accompany it, the
 * expired copy that clears it included. Named as the Go service names it from
 * the same `COOKIE_SECURE`: a Secure cookie takes the `__Host-` prefix, which
 * a browser refuses to accept from a sibling host or with a `Domain`.
 */
export function sessionCookie(): { name: string; path: "/"; secure: boolean } {
  const secure = cookieSecure();
  return { name: secure ? "__Host-dbcontest_session" : "dbcontest_session", path: "/", secure };
}

/**
 * Who the caller is, as `/auth/me` reports it. Route protection branches on
 * permissions, as the server's middleware does; names and roles are for display.
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
 * The session as a header for a server-to-server call, which carries no
 * cookies of its own. Empty when there is no session.
 */
export async function sessionHeader(): Promise<Record<string, string>> {
  const { name } = sessionCookie();
  const session = (await cookies()).get(name)?.value;

  return session ? { cookie: `${name}=${session}` } : {};
}

/**
 * The API could not be asked who the caller is. Unlike a null identity
 * ("nobody"), this is no reason to send anyone to sign-in.
 */
export class IdentityUnavailableError extends Error {
  constructor(readonly status: number) {
    super(`/auth/me could not be reached (status ${status || "none"})`);
    this.name = "IdentityUnavailableError";
  }
}

/**
 * Who the caller is, according to the API; null when there is no usable
 * session. Throws `IdentityUnavailableError` when the API could not be asked,
 * so a restart or a 500 does not become a forced sign-out.
 *
 * React's `cache()` shares one `/auth/me` round trip among the layouts and
 * page of a request. It is per-request, so one visitor's identity never
 * reaches another (a module-level variable would).
 */
export const fetchIdentity = cache(async (): Promise<CurrentIdentity | null> => {
  const header = await sessionHeader();
  if (!header.cookie) return null;

  const response = await fetch(`${apiOrigin()}${API_PREFIX}/auth/me`, {
    headers: { ...(await callerHeaders()), ...header },
    cache: "no-store",
    signal: AbortSignal.timeout(SERVER_REQUEST_TIMEOUT_MS),
  }).catch(() => null);

  return identityFrom(response);
});

/**
 * Reads one `/auth/me` response, apart from the fetch for testing. A null
 * response means the request never completed.
 */
export async function identityFrom(
  response: Response | null,
): Promise<CurrentIdentity | null> {
  // Only a 401 means "no session".
  if (response?.status === 401) return null;

  if (!response?.ok) {
    throw new IdentityUnavailableError(response?.status ?? 0);
  }

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
    // Omitted when the API could not read the account; the login is the fallback.
    fullName: body.full_name ?? "",
    email: body.email,
    roles: body.roles ?? [],
    permissions: body.permissions ?? [],
  };
}

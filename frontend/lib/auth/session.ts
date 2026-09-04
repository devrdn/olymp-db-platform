import { cookies } from "next/headers";
import { cache } from "react";

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
 * Raised when the API could not be asked who the caller is.
 *
 * Distinct from a null identity, and the distinction is the whole point: null
 * means the server answered "nobody", this means it did not answer. Only the
 * first is a reason to send somebody to the sign-in form.
 */
export class IdentityUnavailableError extends Error {
  constructor(readonly status: number) {
    super(`/auth/me could not be reached (status ${status || "none"})`);
    this.name = "IdentityUnavailableError";
  }
}

/**
 * Who the caller is, according to the API.
 *
 * `/auth/me` reports permissions rather than roles, so the interface branches
 * on the same thing the server's middleware does and a role added as data
 * needs no change here. Returns null when there is no usable session, which is
 * what route protection reads.
 *
 * It throws rather than returning null when the question could not be put at
 * all. Treating those the same is how a restarted API, a dropped connection or
 * a 500 becomes a forced sign-out: the visitor's session was fine, and they
 * are told to sign in again — which is the bug that kept being reported as
 * "it throws me to login on every click" and never reproduced, because
 * reproducing it needs the API to blink at the moment somebody navigates.
 *
 * Wrapped in React's `cache()` so the admin layout, the users layout and an
 * account page — each of which calls this to render one request — share one
 * round trip instead of asking `/auth/me` two or three times for the same
 * answer. `cache()` is safe here specifically because its memoization is
 * scoped to one request: React gives each request its own cache with nothing
 * shared between them, so this cannot hand one visitor's identity to
 * another's — unlike a module-level variable, which would, since a Node
 * process serves many requests through the same module instance.
 */
export const fetchIdentity = cache(async (): Promise<CurrentIdentity | null> => {
  const header = await sessionHeader();
  if (!header.cookie) return null;

  const response = await fetch(`${apiOrigin()}${API_PREFIX}/auth/me`, {
    headers: { ...(await callerHeaders()), ...header },
    cache: "no-store",
  }).catch(() => null);

  return identityFrom(response);
});

/**
 * Reads one `/auth/me` response, separated from the fetch so the decision it
 * makes is testable without a framework or a live API.
 *
 * `null` for the response itself means the request never completed.
 */
export async function identityFrom(
  response: Response | null,
): Promise<CurrentIdentity | null> {
  // The one answer that means "no session": the server was asked and said so.
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
    // The API leaves the name out when it could not read the account behind
    // the session — it does not fail the request over a display field. The
    // login is always there, and is what the interface falls back to.
    fullName: body.full_name ?? "",
    email: body.email,
    roles: body.roles ?? [],
    permissions: body.permissions ?? [],
  };
}

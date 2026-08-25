import { cookies } from "next/headers";

import { API_PREFIX } from "@/lib/api/client";

/** Where the API lives from the server's point of view, never the browser's. */
export function apiOrigin(): string {
  return process.env.API_ORIGIN ?? "http://localhost:8080";
}

export const SESSION_COOKIE = "dbcontest_session";

export type CurrentIdentity = { id: string; login: string; permissions: string[] };

/**
 * Who the caller is, according to the API.
 *
 * `/auth/me` reports permissions rather than roles, so the interface branches
 * on the same thing the server's middleware does and a role added as data
 * needs no change here. Returns null when there is no usable session, which is
 * what route protection reads.
 */
export async function fetchIdentity(): Promise<CurrentIdentity | null> {
  const jar = await cookies();
  const session = jar.get(SESSION_COOKIE)?.value;
  if (!session) return null;

  const response = await fetch(`${apiOrigin()}${API_PREFIX}/auth/me`, {
    headers: { cookie: `${SESSION_COOKIE}=${session}` },
    cache: "no-store",
  }).catch(() => null);

  if (!response?.ok) return null;

  const body = (await response.json()) as CurrentIdentity;
  return body;
}

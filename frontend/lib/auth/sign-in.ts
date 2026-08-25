import { API_PREFIX } from "@/lib/api/client";

import { parseSetCookie, type ParsedCookie } from "./cookie";

/**
 * Signing in.
 *
 * This runs on the server, so the API's `Set-Cookie` lands here rather than in
 * the browser and has to be handed on. Both the fetch and the cookie writer are
 * injected, which keeps the whole exchange testable without a framework and
 * without a live API.
 *
 * There is one sign-in for everyone: the API has no separate admin endpoint,
 * and the difference shows up afterwards in the permissions it reports.
 */

export type Credentials = { login: string; password: string };

export type SignInDeps = {
  fetchImpl: typeof fetch;
  setCookie: (cookie: ParsedCookie) => void;
  origin?: string;
};

export type SignInOutcome =
  | { ok: true; mustChangePassword: boolean }
  | { ok: false; code: string };

export async function signIn(
  credentials: Credentials,
  deps: SignInDeps,
): Promise<SignInOutcome> {
  const { fetchImpl, setCookie, origin = "" } = deps;

  const response = await fetchImpl(`${origin}${API_PREFIX}/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(credentials),
  });

  if (!response.ok) {
    // The server answers a wrong login and a wrong password identically, so
    // this code must never be turned into "no such account".
    const body = (await response.json().catch(() => null)) as
      | { error?: { code?: string } }
      | null;
    return { ok: false, code: body?.error?.code ?? "unreachable" };
  }

  const raw = response.headers.getSetCookie?.() ?? [];
  for (const header of raw) {
    const cookie = parseSetCookie(header);
    if (cookie) setCookie(cookie);
  }

  const body = (await response.json()) as { must_change_password?: boolean };
  return { ok: true, mustChangePassword: Boolean(body.must_change_password) };
}

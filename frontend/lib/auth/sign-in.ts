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

/**
 * The cookie the API sets on a browser that has signed in to an account. It
 * has to match `auth.DeviceCookieName` in the Go service.
 */
export const DEVICE_COOKIE = "dbcontest_device";

export type SignInDeps = {
  fetchImpl: typeof fetch;
  setCookie: (cookie: ParsedCookie) => void;
  origin?: string;
  /**
   * Forwarded verbatim, so the API sees who is really signing in. This call
   * leaves from the server, and without the browser's forwarded address the
   * API throttles and audits the web container instead of the person.
   */
  headers?: Record<string, string>;
  /**
   * The browser's device cookie, when it has one. The API throttles a browser
   * the owner has signed in from on its own, rather than with the address a
   * whole lecture hall shares — which it can only do if the cookie reaches it
   * through this server. Only this cookie is sent: the rest of the browser's
   * jar is none of the sign-in's business.
   */
  deviceToken?: string;
  /** How a retry waits. Injected so a test does not wait in real time. */
  sleep?: (ms: number) => Promise<void>;
};

/** The code the API answers when it is too busy to check a password. */
const BUSY = "sign_in_busy";

/**
 * How long a retry after a busy answer may wait at most. The API asks for a
 * second; a header asking for longer is not a reason to hold a person's
 * sign-in open, and past this the form's own answer is the better one.
 */
const MAX_RETRY_WAIT_MS = 5000;

/** The wait when the API names none, or none that can be read. */
const DEFAULT_RETRY_WAIT_MS = 1000;

function retryWait(header: string | null): number {
  const seconds = header === null ? Number.NaN : Number(header.trim());
  if (!Number.isFinite(seconds) || seconds < 0) return DEFAULT_RETRY_WAIT_MS;
  return Math.min(seconds * 1000, MAX_RETRY_WAIT_MS);
}

const realSleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

export type SignInOutcome =
  | { ok: true; mustChangePassword: boolean }
  | { ok: false; code: string };

export async function signIn(
  credentials: Credentials,
  deps: SignInDeps,
): Promise<SignInOutcome> {
  const { fetchImpl, setCookie, origin = "", headers = {}, deviceToken, sleep = realSleep } = deps;

  const attempt = () =>
    fetchImpl(`${origin}${API_PREFIX}/auth/login`, {
      method: "POST",
      headers: {
        ...headers,
        ...(deviceToken ? { cookie: `${DEVICE_COOKIE}=${deviceToken}` } : {}),
        "content-type": "application/json",
      },
      body: JSON.stringify(credentials),
    });

  let response = await attempt();
  let failure = response.ok ? null : await failureCode(response);

  // Too busy to check the password is not a verdict on it: the person typed
  // nothing wrong. So the sign-in waits as long as the API asks — bounded —
  // and tries exactly once more before saying so. Once, because a second
  // refusal means the load is not a moment's, and repeating would only add to
  // it.
  if (failure === BUSY) {
    await sleep(retryWait(response.headers.get("retry-after")));
    response = await attempt();
    failure = response.ok ? null : await failureCode(response);
  }

  if (failure !== null) return { ok: false, code: failure };

  const raw = response.headers.getSetCookie?.() ?? [];
  for (const header of raw) {
    const cookie = parseSetCookie(header);
    if (cookie) setCookie(cookie);
  }

  const body = (await response.json()) as { must_change_password?: boolean };
  return { ok: true, mustChangePassword: Boolean(body.must_change_password) };
}

/**
 * The code of a refused sign-in. The server answers a wrong login and a wrong
 * password identically, so this code must never be turned into "no such
 * account".
 */
async function failureCode(response: Response): Promise<string> {
  const body = (await response.json().catch(() => null)) as { error?: { code?: string } } | null;
  return body?.error?.code ?? "unreachable";
}

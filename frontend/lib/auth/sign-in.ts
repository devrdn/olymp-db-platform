import { API_PREFIX } from "@/lib/api/client";

import { parseSetCookie, type ParsedCookie } from "./cookie";

/**
 * Signing in, on the server: the API's `Set-Cookie` arrives here and has to be
 * handed on to the browser. Fetch and cookie writer are injected for testing.
 * There is one sign-in for everyone; staff differ only in permissions.
 */

export type Credentials = { login: string; password: string };

/** Must match `auth.DeviceCookieName` in the Go service. */
export const DEVICE_COOKIE = "dbcontest_device";

export type SignInDeps = {
  fetchImpl: typeof fetch;
  setCookie: (cookie: ParsedCookie) => void;
  origin?: string;
  /** The forwarded client headers, so the API throttles and audits the person. */
  headers?: Record<string, string>;
  /**
   * The browser's device cookie, so the API can throttle a known browser on
   * its own rather than with a whole lecture hall's shared address. Only this
   * cookie is sent.
   */
  deviceToken?: string;
  sleep?: (ms: number) => Promise<void>;
};

/** The API is too busy to check a password. */
const BUSY = "sign_in_busy";

/** A longer `Retry-After` is not worth holding a sign-in open for. */
const MAX_RETRY_WAIT_MS = 5000;

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

  // "Busy" is not a verdict on the password: wait as asked (bounded) and retry
  // once. A second refusal means sustained load, which retries would add to.
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
 * A wrong login and a wrong password get the same code; never turn it into
 * "no such account".
 */
async function failureCode(response: Response): Promise<string> {
  const body = (await response.json().catch(() => null)) as { error?: { code?: string } } | null;
  return body?.error?.code ?? "unreachable";
}

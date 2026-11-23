import { z } from "zod";

import { API_PREFIX, apiErrorOf } from "@/lib/api/client";
import { outcome, type Outcome } from "@/lib/api/outcome";
import { SERVER_REQUEST_TIMEOUT_MS } from "@/lib/api/server";

import { cookiePair, parseSetCookie, type ParsedCookie } from "./cookie";
import { sessionCookie } from "./session";

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
   * its own rather than with a whole lecture hall's shared address.
   */
  deviceToken?: string;
  /**
   * The browser's current session cookie, if it still has one: the API ends
   * that session when it issues the new one, so signing in again does not
   * leave the previous token valid until it expires. Nothing else of the jar
   * is sent.
   */
  sessionToken?: string;
  sleep?: (ms: number) => Promise<void>;
};

/** The API is too busy to check a password. */
const BUSY = "sign_in_busy";

/** A longer `Retry-After` is not worth holding a sign-in open for. */
const MAX_RETRY_WAIT_MS = 5000;

const DEFAULT_RETRY_WAIT_MS = 1000;

function retryWait(seconds: number | undefined): number {
  return seconds === undefined ? DEFAULT_RETRY_WAIT_MS : Math.min(seconds * 1000, MAX_RETRY_WAIT_MS);
}

/** The answer to a sign-in that worked; read before any cookie is handed on. */
const signedInSchema = z.object({ must_change_password: z.boolean().optional() });

const realSleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/**
 * Signed in, or the refusal's code alone. A wrong login and a wrong password
 * get the same code; never turn it into "no such account".
 */
export type SignInOutcome = Outcome<{ mustChangePassword: boolean }>;

export async function signIn(
  credentials: Credentials,
  deps: SignInDeps,
): Promise<SignInOutcome> {
  const { fetchImpl, setCookie, origin = "", headers = {}, deviceToken, sessionToken, sleep = realSleep } = deps;

  const cookie = [cookiePair(DEVICE_COOKIE, deviceToken), cookiePair(sessionCookie().name, sessionToken)]
    .filter((pair) => pair !== null)
    .join("; ");

  // Each attempt its own deadline, as every other server call has. The body is
  // read inside it, so an answer this side cannot read is `unreachable` before
  // any cookie is set, not a session issued to a browser told nothing.
  const attempt = () =>
    outcome(async () => {
      const response = await fetchImpl(`${origin}${API_PREFIX}/auth/login`, {
        method: "POST",
        headers: {
          ...headers,
          ...(cookie ? { cookie } : {}),
          "content-type": "application/json",
        },
        body: JSON.stringify(credentials),
        signal: AbortSignal.timeout(SERVER_REQUEST_TIMEOUT_MS),
      });
      if (!response.ok) throw await apiErrorOf(response);
      const body = signedInSchema.parse(await response.json());
      return {
        cookies: response.headers.getSetCookie?.() ?? [],
        mustChangePassword: body.must_change_password ?? false,
      };
    });

  let answer = await attempt();

  // "Busy" is not a verdict on the password: wait as asked (bounded) and retry
  // once. A second refusal means sustained load, which retries would add to.
  if (!answer.ok && answer.code === BUSY) {
    await sleep(retryWait(answer.retryAfterSeconds));
    answer = await attempt();
  }

  if (!answer.ok) return { ok: false, code: answer.code };

  for (const header of answer.value.cookies) {
    const cookie = parseSetCookie(header);
    if (cookie) setCookie(cookie);
  }
  return { ok: true, value: { mustChangePassword: answer.value.mustChangePassword } };
}

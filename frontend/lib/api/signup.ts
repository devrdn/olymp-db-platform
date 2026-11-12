import { z } from "zod";

import { parseSetCookie, type ParsedCookie } from "@/lib/auth/cookie";
import { cookieSecure } from "@/lib/auth/cookie-policy";
import type { SignupCommand } from "@/lib/auth/signup-rules";

import { failureCode, request } from "./client";
import { SERVER_REQUEST_TIMEOUT_MS } from "./server";

/**
 * Self-service sign-up, on the server. The token binding a sign-up to its
 * browser travels only in the sign-up cookie, which the API sets and this
 * server hands on, as sign-in hands on the session (`lib/auth/sign-in.ts`):
 * the API's answer arrives here, not in the browser.
 */

/** Named as `auth.CookieWriter.SignupName` names it from the same `COOKIE_SECURE`. */
export function signupCookieName(): string {
  return cookieSecure() ? "__Host-dbcontest_signup" : "dbcontest_signup";
}

/** Start and resend answer alike, for an address an account holds as for a new one. */
export const signupSentSchema = z
  .object({ status: z.literal("code_sent"), email: z.string() })
  .transform((body) => ({ email: body.email }));

export const signupConfirmedSchema = z
  .object({ must_change_password: z.boolean() })
  .transform((body) => ({ mustChangePassword: body.must_change_password }));

/** Where to reach the API and what of the browser's request goes with it. */
export type Relay = {
  origin: string;
  /** The forwarded client headers, so the API throttles and audits the person. */
  headers: Record<string, string>;
  /** The browser's cookies the API reads, as one `Cookie` header; nothing else of the jar. */
  cookie?: string;
  /** Hands one of the API's cookies to the browser. */
  setCookie: (cookie: ParsedCookie) => void;
  fetchImpl?: typeof fetch;
};

/**
 * Posts and hands every cookie of the answer on, a refusal's included: a
 * confirmation spends the sign-up, and clears its cookie, even when the
 * sign-in after it fails. A failure is thrown as an `ApiError`.
 */
async function relayed(path: string, body: unknown, relay: Relay): Promise<unknown> {
  const { origin, headers, cookie, setCookie, fetchImpl = fetch } = relay;
  let answered: Response | undefined;

  try {
    return await request(path, {
      method: "POST",
      body,
      origin,
      headers: cookie ? { ...headers, cookie } : headers,
      signal: AbortSignal.timeout(SERVER_REQUEST_TIMEOUT_MS),
      fetchImpl: async (input, init) => (answered = await fetchImpl(input, init)),
    });
  } finally {
    for (const header of answered?.headers.getSetCookie?.() ?? []) {
      const parsed = parseSetCookie(header);
      if (parsed) setCookie(parsed);
    }
  }
}

/** `lang` picks the language of the mail with the code. */
export async function startSignup(command: SignupCommand, lang: string, relay: Relay) {
  return signupSentSchema.parse(await relayed(`/public/signup?lang=${encodeURIComponent(lang)}`, command, relay));
}

export async function confirmSignup(input: { email: string; code: string }, relay: Relay) {
  return signupConfirmedSchema.parse(await relayed("/public/signup/confirm", input, relay));
}

export async function resendSignup(input: { email: string }, lang: string, relay: Relay) {
  return signupSentSchema.parse(
    await relayed(`/public/signup/resend?lang=${encodeURIComponent(lang)}`, input, relay),
  );
}

/**
 * The code a sign-up failure is answered with. The routes are not mounted
 * when the installation does not offer sign-up, so the router's `not_found`
 * means exactly that.
 */
export function signupAnswerCode(error: unknown): string {
  const code = failureCode(error);
  return code === "not_found" ? "signup_not_available" : code;
}

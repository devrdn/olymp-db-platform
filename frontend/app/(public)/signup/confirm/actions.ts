"use server";

import { redirect } from "next/navigation";

import { confirmSignup, resendSignup, signupAnswerCode, signupCookieName } from "@/lib/api/signup";
import { ApiError } from "@/lib/api/client";
import { destinationAfterLogin } from "@/lib/auth/destination";
import { browserRelay } from "@/lib/auth/relay";
import { fetchIdentity, sessionCookie } from "@/lib/auth/session";
import { forgetSignupEmail, rememberSignupEmail, signupEmail } from "@/lib/auth/signup-email";
import { isSignupCode, normalizeCode } from "@/lib/auth/signup-rules";
import { activeLocale } from "@/lib/i18n/server";

export type ConfirmState = { code?: string };

/**
 * With no address kept, there is no sign-up in this browser to confirm, and
 * the way on is to start one: the sentence and the page say so.
 */
const NOTHING_TO_CONFIRM = "signup_code_expired";

/**
 * Confirms the mailed code, which makes the account and signs it in. The
 * address is read from its own cookie, never from the form. The browser's
 * sign-up cookie proves this is the browser that signed up; its session, if
 * any, goes too, so the API ends it rather than leaving it valid beside the
 * new one. The API's answer sets the session and device cookies and clears
 * the sign-up cookie, all handed on.
 */
export async function confirmSignupAction(_previous: ConfirmState, form: FormData): Promise<ConfirmState> {
  const email = await signupEmail();
  const code = normalizeCode(String(form.get("code") ?? ""));

  if (!email) return { code: NOTHING_TO_CONFIRM };
  // Every code the API checks spends one of the sign-up's few tries.
  if (!isSignupCode(code)) return { code: "signup_wrong_code" };

  const relay = await browserRelay([signupCookieName(), sessionCookie().name]);

  // `.then(ok, failed)`, not try/catch: `redirect` throws, and a catch would
  // swallow it.
  const outcome = await confirmSignup({ email, code }, relay).then(
    (confirmed) => ({ ok: true as const, mustChangePassword: confirmed.mustChangePassword }),
    (error: unknown) => ({ ok: false as const, code: signupAnswerCode(error) }),
  );
  if (!outcome.ok) return { code: outcome.code };

  await forgetSignupEmail();

  // As after sign-in: the session exists either way, so a failed read lands
  // on the screen every account can open.
  const identity = await fetchIdentity().catch(() => null);
  redirect(
    destinationAfterLogin({
      mustChangePassword: outcome.mustChangePassword,
      permissions: identity?.permissions ?? [],
    }),
  );
}

export type ResendOutcome = { sent: true } | { sent: false; code: string; retryAfterSeconds?: number };

/**
 * Mails a fresh code to the sign-up this browser started, at the address
 * kept for it. Called from the button's handler rather than bound to a form:
 * the countdown that gates it needs JavaScript anyway.
 */
export async function resendCodeAction(): Promise<ResendOutcome> {
  const email = await signupEmail();
  if (!email) return { sent: false, code: NOTHING_TO_CONFIRM };

  const [relay, lang] = await Promise.all([browserRelay([signupCookieName()]), activeLocale()]);

  try {
    await resendSignup({ email }, lang, relay);
    // The new code's window is the address's too.
    await rememberSignupEmail(email);
    return { sent: true };
  } catch (error) {
    const retryAfterSeconds = error instanceof ApiError ? error.retryAfterSeconds : undefined;
    return retryAfterSeconds === undefined
      ? { sent: false, code: signupAnswerCode(error) }
      : { sent: false, code: signupAnswerCode(error), retryAfterSeconds };
  }
}

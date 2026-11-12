"use server";

import { redirect } from "next/navigation";

import { signupAnswerCode, startSignup } from "@/lib/api/signup";
import { browserRelay } from "@/lib/auth/relay";
import { checkSignup, normalizeLogin, type SignupField } from "@/lib/auth/signup-rules";
import { activeLocale } from "@/lib/i18n/server";

export type SignUpValues = { fullName: string; login: string; email: string };

/**
 * A code, and the field whose rule was broken when this side knows it, with
 * what was typed: React resets an action's form. The passwords are never
 * returned.
 */
export type SignUpState = { code?: string; field?: SignupField; values?: SignUpValues };

/**
 * Starts a sign-up. The API mails a code and sets the sign-up cookie, which is
 * handed to the browser; the confirmation page needs only the address.
 */
export async function signUpAction(_previous: SignUpState, form: FormData): Promise<SignUpState> {
  const input = {
    fullName: String(form.get("full_name") ?? ""),
    login: String(form.get("login") ?? ""),
    email: String(form.get("email") ?? ""),
    password: String(form.get("password") ?? ""),
    confirm: String(form.get("confirm") ?? ""),
  };
  const values: SignUpValues = {
    fullName: input.fullName.trim(),
    login: normalizeLogin(input.login),
    email: input.email.trim(),
  };

  const check = checkSignup(input);
  if (!check.ok) {
    // The broken rule is shown against what was typed, as it was typed.
    return { code: check.code, field: check.field, values: { ...values, login: input.login.trim() } };
  }

  const [relay, lang] = await Promise.all([browserRelay([]), activeLocale()]);

  // `.then(ok, failed)`, not try/catch: `redirect` throws, and a catch would
  // swallow it.
  const outcome = await startSignup(check.command, lang, relay).then(
    (sent) => ({ ok: true as const, email: sent.email }),
    (error: unknown) => ({ ok: false as const, code: signupAnswerCode(error) }),
  );
  if (!outcome.ok) return { code: outcome.code, values };

  redirect(`/signup/confirm?email=${encodeURIComponent(outcome.email)}`);
}

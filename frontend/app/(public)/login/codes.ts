import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * Not a client module: the page, a Server Component, calls these, and a
 * function exported from a `"use client"` file would reach it as a reference.
 */

/** What the external sign-in's callback puts in `/login?error=`. */
const EXTERNAL_SIGN_IN_ERRORS = [
  "external_signin_failed",
  "external_signin_staff_account",
  "external_signin_closed",
  "external_signin_conflict",
  "account_blocked",
] as const;

export type ExternalSignInError = (typeof EXTERNAL_SIGN_IN_ERRORS)[number];

/**
 * The `?error=` value when it is one of the callback's codes. Anything else,
 * a repeated parameter included, is null: the address selects a sentence and
 * never supplies one.
 */
export function externalSignInError(value: string | string[] | undefined): ExternalSignInError | null {
  return (EXTERNAL_SIGN_IN_ERRORS as readonly unknown[]).includes(value) ? (value as ExternalSignInError) : null;
}

/**
 * What a password sign-in can end in: the login handler's refusals, a malformed
 * body, a cross-site write, a failure, and the action's own `unreachable`. A
 * code outside this list reads as the fallback sentence.
 */
const SIGN_IN_FAILURES = [
  "invalid_request",
  "invalid_credentials",
  "account_blocked",
  "too_many_attempts",
  "sign_in_busy",
  "cross_origin",
  "internal_error",
  "unreachable",
  "fallback",
] as const;

export type SignInErrors = Pick<Dictionary["errors"], (typeof SIGN_IN_FAILURES)[number]>;

/**
 * The sentences the sign-in form can show. The form is a Client Component on
 * the most visited page, and the whole `errors` section would be serialised
 * into its payload.
 */
export function signInErrors(errors: Dictionary["errors"]): SignInErrors {
  return Object.fromEntries(SIGN_IN_FAILURES.map((code) => [code, errors[code]])) as SignInErrors;
}

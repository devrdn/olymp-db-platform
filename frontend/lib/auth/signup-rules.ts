/**
 * The sign-up form's own checks, before the API is asked: the rules a person
 * is shown up front, and the confirmation match the API never sees. The API
 * stays the authority (`internal/signup`); these only spare a round trip and
 * say which field broke which rule. The password's strength stays on the Go
 * side, as on the change-password screen, so the two cannot drift. Free of
 * zod, so the form can import the bounds.
 */

/** `signup.plainLogin` allows 3 to 64 characters. */
export const LOGIN_MAX_CHARS = 64;

/** `users.MaxFullNameLength`. */
export const FULL_NAME_MAX_CHARS = 200;

/** `users.MaxEmailLength`. */
export const EMAIL_MAX_CHARS = 254;

/** `signup.CodeDigits`. */
export const SIGNUP_CODE_DIGITS = 6;

/** `signup.ResendCooldown`: how long after a code was mailed another may be. */
export const RESEND_COOLDOWN_SECONDS = 60;

/** The same pattern as `signup.plainLogin`. */
const PLAIN_LOGIN = /^[a-z0-9][a-z0-9_-]{2,63}$/;

/** `users.IsWrittenAsAName`: letters and marks, space, hyphen, both apostrophes, period. */
const OF_A_NAME = /^[\p{L}\p{M} '’.-]+$/u;

/** A period directly before a letter (`J.R`), which the API also refuses. */
const PERIOD_BEFORE_LETTER = /\.\p{L}/u;

export type SignupField = "fullName" | "login" | "email" | "password" | "confirm";

/** The login as the API keys it. */
export function normalizeLogin(raw: string): string {
  return raw.trim().toLowerCase();
}

export function isPlainLogin(login: string): boolean {
  return PLAIN_LOGIN.test(login);
}

/** Bounded in characters, as the person is told. */
export function isWrittenAsName(name: string): boolean {
  return (
    name !== "" &&
    [...name].length <= FULL_NAME_MAX_CHARS &&
    OF_A_NAME.test(name) &&
    !PERIOD_BEFORE_LETTER.test(name)
  );
}

export type SignupInput = {
  fullName: string;
  login: string;
  email: string;
  password: string;
  confirm: string;
};

export type SignupCommand = { full_name: string; login: string; email: string; password: string };

export type SignupCheck =
  | { ok: true; command: SignupCommand }
  | { ok: false; code: string; field?: SignupField };

/**
 * Nothing in a password is trimmed: a space is part of the secret. A broken
 * rule names its field, so the form can say the rule itself.
 */
export function checkSignup(input: SignupInput): SignupCheck {
  const fullName = input.fullName.trim();
  const login = normalizeLogin(input.login);

  if (!isWrittenAsName(fullName)) return { ok: false, code: "invalid_request", field: "fullName" };
  if (!isPlainLogin(login)) return { ok: false, code: "invalid_request", field: "login" };
  if (input.password !== input.confirm) return { ok: false, code: "password_mismatch" };

  return {
    ok: true,
    command: { full_name: fullName, login, email: input.email.trim(), password: input.password },
  };
}

/** A typed or pasted code without its spaces. */
export function normalizeCode(raw: string): string {
  return raw.replace(/\s+/g, "");
}

export function isSignupCode(code: string): boolean {
  return new RegExp(`^\\d{${SIGNUP_CODE_DIGITS}}$`).test(code);
}

/** One `@`, something on each side, no whitespace or control characters. */
const ADDRESS_SHAPE = /^[^\s@\p{Cc}]+@[^\s@\p{Cc}]+$/u;

/**
 * The address from the confirmation page's query, if it may be repeated in
 * a sentence. The address bar may select a sentence but never write one, so
 * anything that is not shaped like an address is withheld.
 */
export function showableEmail(raw: string): string | undefined {
  return raw.length <= EMAIL_MAX_CHARS && ADDRESS_SHAPE.test(raw) ? raw : undefined;
}

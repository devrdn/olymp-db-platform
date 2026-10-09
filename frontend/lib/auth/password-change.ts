/**
 * The password-change form's own check: only the confirmation match, which
 * the server never sees. Strength rules stay on the Go side (`weak_password`,
 * `same_password`) so the two cannot drift. Nothing is trimmed: a space is
 * part of the secret.
 */

export type PasswordChangeInput = {
  /** One-time or otherwise. */
  current: string;
  next: string;
  confirm: string;
};

export type PasswordChangeCommand = { old_password: string; new_password: string };

export type PasswordChangeCheck =
  | { ok: true; command: PasswordChangeCommand }
  | { ok: false; code: string };

export function checkPasswordChange(input: PasswordChangeInput): PasswordChangeCheck {
  const { current, next, confirm } = input;

  // Mismatch first: when both apply, the typo is the actionable half.
  if (next !== confirm) return { ok: false, code: "password_mismatch" };
  if (current === "" || next === "") return { ok: false, code: "invalid_request" };

  return { ok: true, command: { old_password: current, new_password: next } };
}

/**
 * The rules of the password-change form, separated from the request that
 * carries it.
 *
 * Only one rule lives here, and it is the one the API structurally cannot
 * apply: the confirmation field never leaves the browser, so nothing on the
 * server can tell the author they typed it twice differently. Everything else
 * — length, variety, whether the new password repeats the old — stays on the
 * Go side and comes back as `weak_password` or `same_password`. Re-stating
 * those rules here would create a second source of truth that drifts from the
 * first the moment either moves, and the symptom would be a form that refuses
 * a password the server would have accepted.
 *
 * Nothing is trimmed. A leading or trailing space is a character of the
 * secret, and an administrator handing over a one-time password has no way of
 * knowing this form quietly edited it.
 */

export type PasswordChangeInput = {
  /** The password the account currently has — one-time or otherwise. */
  current: string;
  next: string;
  confirm: string;
};

/** The wire shape of `POST /auth/password`, in the API's own snake_case. */
export type PasswordChangeCommand = { old_password: string; new_password: string };

export type PasswordChangeCheck =
  | { ok: true; command: PasswordChangeCommand }
  | { ok: false; code: string };

export function checkPasswordChange(input: PasswordChangeInput): PasswordChangeCheck {
  const { current, next, confirm } = input;

  // The mismatch is reported ahead of the emptiness on purpose. When both are
  // true the actionable half is the typo; being told "fill in the field" while
  // staring at a filled one sends the author to the wrong field.
  if (next !== confirm) return { ok: false, code: "password_mismatch" };
  if (current === "" || next === "") return { ok: false, code: "invalid_request" };

  return { ok: true, command: { old_password: current, new_password: next } };
}

"use server";

import { deleteAccount, resetPassword, restoreAccount } from "@/lib/api/accounts";
import { isId } from "@/lib/api/ids";
import { serverAction, serverRequest, type ActionState, type Revalidation } from "@/lib/api/server";

import { sendByEmail } from "../delivery";

/**
 * Server Actions on one account: they work without JavaScript, keep the API
 * origin off the page, and get Next's Origin check. The server enforces every
 * rule again; `offered.ts` only decides what to show.
 */

export type AccountState = ActionState;

/** The id reaches a request path, so it is validated first. */
function subject(form: FormData): string | null {
  const userId = form.get("userId");
  return isId(userId) ? userId : null;
}

/** Both the card and the register list. */
const CHANGED: Revalidation[] = [{ layout: "/users" }];

export async function updateProfileAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  const fullName = String(form.get("full_name") ?? "").trim();
  if (fullName === "") return { code: "invalid_request" };

  return serverAction(
    () =>
      serverRequest(`/users/${userId}`, {
        method: "PATCH",
        body: { full_name: fullName, email: String(form.get("email") ?? "").trim() },
      }),
    { revalidate: CHANGED },
  );
}

export async function replaceRolesAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  // An empty set is legitimate: an account with no role can sign in and do
  // nothing.
  const roles = form.getAll("roles").map(String).filter(Boolean);

  return serverAction(() => serverRequest(`/users/${userId}/roles`, { method: "PUT", body: { roles } }), {
    revalidate: CHANGED,
  });
}

/** The required reason, or null when empty or whitespace-only. */
function requiredReason(form: FormData): string | null {
  const reason = String(form.get("reason") ?? "").trim();
  return reason === "" ? null : reason;
}

export async function blockAction(_previous: AccountState, form: FormData): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  // Required by the server (`users.ErrReasonRequired`); checked before the
  // request leaves.
  const reason = requiredReason(form);
  if (reason === null) return { code: "reason_required" };

  return serverAction(() => serverRequest(`/users/${userId}/block`, { method: "POST", body: { reason } }), {
    revalidate: CHANGED,
  });
}

export async function unblockAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  // Unblocking needs no reason (`users.Service.Unblock`).
  return serverAction(() => serverRequest(`/users/${userId}/unblock`, { method: "POST" }), { revalidate: CHANGED });
}

/** Soft-deletes the account; requires a reason, like blocking. */
export async function deleteAction(_previous: AccountState, form: FormData): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  const reason = requiredReason(form);
  if (reason === null) return { code: "reason_required" };

  return serverAction(() => deleteAccount(userId, reason), { revalidate: CHANGED });
}

/**
 * Reverses a soft delete. Fails with `login_taken` or `email_taken` when a live
 * account has since claimed either, and `Outcome` names which.
 */
export async function restoreAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  return serverAction(() => restoreAccount(userId), { revalidate: CHANGED });
}

export type ResetState = AccountState & {
  oneTimePassword?: string;
  /** Whether the sign-in details were queued by email. */
  emailed?: boolean;
  /** Why they were not, when email was asked for (`users.EmailSkip*`). */
  emailSkippedReason?: string;
};

/**
 * Issues a one-time password, and emails it when the box is ticked. It is
 * returned once and shown until the administrator leaves; never put in the
 * address or logged, since URLs land in history and access logs.
 */
export async function resetPasswordAction(
  _previous: ResetState,
  form: FormData,
): Promise<ResetState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  return serverAction(() => resetPassword(userId, sendByEmail(form)), {
    revalidate: CHANGED,
    success: (issued) => ({
      ok: true,
      oneTimePassword: issued.one_time_password,
      emailed: issued.emailed,
      emailSkippedReason: issued.email_skipped_reason,
    }),
  });
}

/**
 * Clears the account's sign-in lockout at every address, so an owner locked out
 * by someone behind the same address can retry. Per-address counters are
 * untouched; the server records who did this.
 */
export async function unlockSignInAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  return serverAction(() => serverRequest(`/users/${userId}/sign-in/unlock`, { method: "POST" }), {
    revalidate: CHANGED,
  });
}

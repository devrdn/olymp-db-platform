"use server";

import { revalidatePath } from "next/cache";

import { deleteAccount, passwordResetSchema, restoreAccount } from "@/lib/api/accounts";
import { ApiError, failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

/**
 * Server Actions on one account: they work without JavaScript, keep the API
 * origin off the page, and get Next's Origin check. The server enforces every
 * rule again; `offered.ts` only decides what to show.
 */

export type AccountState = { code?: string; done?: boolean };

/** The id reaches a request path, so it is validated first. */
function subject(form: FormData): string | null {
  const userId = form.get("userId");
  return isId(userId) ? userId : null;
}

/**
 * Runs one request, maps a failure to a code `Outcome` in `account-card.tsx`
 * knows, and revalidates on success. Takes the request itself so delete and
 * restore reuse `lib/api/accounts`.
 */
async function attempt(action: () => Promise<unknown>): Promise<AccountState> {
  const failure = await action().then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // Both the card and the register list.
  revalidatePath("/users", "layout");
  return { done: true };
}

export async function updateProfileAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  const fullName = String(form.get("full_name") ?? "").trim();
  if (fullName === "") return { code: "invalid_request" };

  return attempt(() =>
    serverRequest(`/users/${userId}`, {
      method: "PATCH",
      body: { full_name: fullName, email: String(form.get("email") ?? "").trim() },
    }),
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

  return attempt(() => serverRequest(`/users/${userId}/roles`, { method: "PUT", body: { roles } }));
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

  return attempt(() => serverRequest(`/users/${userId}/block`, { method: "POST", body: { reason } }));
}

export async function unblockAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  // Unblocking needs no reason (`users.Service.Unblock`).
  return attempt(() => serverRequest(`/users/${userId}/unblock`, { method: "POST" }));
}

/** Soft-deletes the account; requires a reason, like blocking. */
export async function deleteAction(_previous: AccountState, form: FormData): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  const reason = requiredReason(form);
  if (reason === null) return { code: "reason_required" };

  return attempt(() => deleteAccount(userId, reason));
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

  return attempt(() => restoreAccount(userId));
}

export type ResetState = AccountState & { oneTimePassword?: string };

/**
 * Issues a one-time password. It is returned once and shown until the
 * administrator leaves; never put in the address or logged, since URLs land in
 * history and access logs.
 */
export async function resetPasswordAction(
  _previous: ResetState,
  form: FormData,
): Promise<ResetState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  const outcome = await serverRequest(`/users/${userId}/password-reset`, { method: "POST" }).then(
    (payload) => ({ ok: true as const, body: passwordResetSchema.parse(payload) }),
    (error: unknown) => ({ ok: false as const, error }),
  );

  if (!outcome.ok) {
    return { code: outcome.error instanceof ApiError ? outcome.error.code : "unreachable" };
  }

  revalidatePath("/users", "layout");
  return { done: true, oneTimePassword: outcome.body.one_time_password };
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

  return attempt(() => serverRequest(`/users/${userId}/sign-in/unlock`, { method: "POST" }));
}

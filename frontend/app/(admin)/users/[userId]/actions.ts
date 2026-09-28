"use server";

import { revalidatePath } from "next/cache";

import { deleteAccount, passwordResetSchema, restoreAccount } from "@/lib/api/accounts";
import { ApiError, failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

/**
 * What an administrator can do to one account.
 *
 * Server Actions rather than fetches from the browser: the forms work with
 * JavaScript switched off, the API's origin never reaches the page, and Next
 * checks the request's Origin against its Host before any of this runs.
 *
 * Every one of these is refused again by the server. What the screen decides
 * (see `offered.ts`) is only what to put in front of somebody.
 */

export type AccountState = { code?: string; done?: boolean };

/** The identifier reaches a request path, so it is checked before it does. */
function subject(form: FormData): string | null {
  const userId = form.get("userId");
  return isId(userId) ? userId : null;
}

/**
 * Runs one request against an account, translates the failure into what
 * `Outcome` in `account-card.tsx` looks up, and revalidates on success.
 *
 * Takes the request itself rather than a method and a path: block and unblock
 * are one raw call each, but delete and restore go through `deleteAccount`
 * and `restoreAccount` from `lib/api/accounts` instead of reassembling their
 * routes here — the same functions `../bulk-actions.ts` reaches for its own
 * calls, so the route is spent once rather than invented at every call site.
 */
async function attempt(action: () => Promise<unknown>): Promise<AccountState> {
  const failure = await action().then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // Both the card and the register behind it: a blocked account has to stop
  // reading "active" on the list somebody returns to.
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

  // Every checked box, and an unchecked set is a legitimate answer: an account
  // with no role can sign in and do nothing, which is sometimes exactly what
  // somebody wants while they sort out who a person is.
  const roles = form.getAll("roles").map(String).filter(Boolean);

  return attempt(() => serverRequest(`/users/${userId}/roles`, { method: "PUT", body: { roles } }));
}

/** The reason a status change is refused to leave the screen without: empty or whitespace-only. */
function requiredReason(form: FormData): string | null {
  const reason = String(form.get("reason") ?? "").trim();
  return reason === "" ? null : reason;
}

export async function blockAction(_previous: AccountState, form: FormData): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  // The server has required a reason since early in this branch
  // (`users.ErrReasonRequired`); this card used to send none at all, which
  // made every block from here fail. Caught before the request leaves, the
  // same way the bulk block dialog catches it.
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

  // Returning to the ordinary state needs no justification — see
  // `users.Service.Unblock`.
  return attempt(() => serverRequest(`/users/${userId}/unblock`, { method: "POST" }));
}

/**
 * Soft-deletes the account. Same rule as blocking: an empty or
 * whitespace-only reason never reaches the server.
 */
export async function deleteAction(_previous: AccountState, form: FormData): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  const reason = requiredReason(form);
  if (reason === null) return { code: "reason_required" };

  return attempt(() => deleteAccount(userId, reason));
}

/**
 * Reverses a soft delete. Can fail with `login_taken` or `email_taken` when a
 * live account has since claimed the login or the email — the direct price of
 * releasing them on deletion — and `Outcome` already renders each under its
 * own name, so the administrator learns which one to resolve rather than
 * being told only that the restore failed.
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
 * Issuing a fresh one-time password.
 *
 * The password comes back exactly once and is never retrievable again — a lost
 * one is another reset. So it is returned to the screen and shown there until
 * the administrator navigates away, rather than flashed in a toast that a
 * glance can miss.
 *
 * It is deliberately not put in the address and not logged: a URL lands in
 * browser history and in the proxy's access log, and this is a credential
 * until its owner replaces it.
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
 * Clears the sign-in lockout of an account: the failed attempts counted
 * against it, at every address, are forgotten, so its owner — shut out by a
 * rival behind the same lab address, say — can try again now. Attempts counted
 * against an address are not touched; the server records who did this.
 */
export async function unlockSignInAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  return attempt(() => serverRequest(`/users/${userId}/sign-in/unlock`, { method: "POST" }));
}

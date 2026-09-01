"use server";

import { revalidatePath } from "next/cache";

import { passwordResetSchema } from "@/lib/api/accounts";
import { ApiError } from "@/lib/api/client";
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

async function attempt(
  userId: string,
  init: { method: string; body?: unknown },
  path = "",
): Promise<AccountState> {
  const failure = await serverRequest(`/users/${userId}${path}`, init).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

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

  return attempt(userId, {
    method: "PATCH",
    body: { full_name: fullName, email: String(form.get("email") ?? "").trim() },
  });
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

  return attempt(userId, { method: "PUT", body: { roles } }, "/roles");
}

export async function blockAction(_previous: AccountState, form: FormData): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  return attempt(userId, { method: "POST" }, "/block");
}

export async function unblockAction(
  _previous: AccountState,
  form: FormData,
): Promise<AccountState> {
  const userId = subject(form);
  if (!userId) return { code: "invalid_user_id" };

  return attempt(userId, { method: "POST" }, "/unblock");
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

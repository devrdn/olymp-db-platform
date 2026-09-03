import { z } from "zod";

import { ACCOUNT_STATUSES, type AccountStatus } from "./accounts-terms";
import { serverRequest } from "./server";

export {
  ACCOUNT_STATUSES,
  type AccountStatus,
  SKIP_REASONS,
  type SkipReason,
  MAX_BULK_ACCOUNTS,
} from "./accounts-terms";

/**
 * The wire shapes of an account and of the roles it may hold.
 *
 * Parsing at the boundary means a contract change surfaces here, with the
 * field name in the message, instead of as `undefined` three components later.
 * The API speaks snake_case; the interface speaks camelCase, and the mapping
 * happens once, here.
 */


export const accountSchema = z
  .object({
    id: z.string(),
    login: z.string(),
    email: z.string().optional(),
    full_name: z.string(),
    status: z.enum(ACCOUNT_STATUSES),
    roles: z.array(z.string()).default([]),
    must_change_password: z.boolean().default(false),
    last_login_at: z.string().optional(),
    created_at: z.string(),
    // All three empty for an account nobody has ever blocked or deleted — see
    // `UserResponse` in `backend/internal/api/users_handler.go`.
    status_reason: z.string().optional(),
    status_changed_at: z.string().optional(),
    status_changed_by: z.string().optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    login: raw.login,
    email: raw.email,
    fullName: raw.full_name,
    status: raw.status,
    /**
     * Codes, not names. The catalogue below turns them into something a
     * person reads, and the two are looked up together rather than the server
     * sending each account's role names on every row.
     */
    roles: raw.roles,
    /** Still on the password an administrator handed over. */
    mustChangePassword: raw.must_change_password,
    lastLoginAt: raw.last_login_at,
    createdAt: raw.created_at,
    /** "" for an account nobody has blocked or deleted — never rendered as a frame. */
    statusReason: raw.status_reason ?? "",
    statusChangedAt: raw.status_changed_at,
    /** The changing actor's id. The account card resolves it to a name by asking `/users/{id}` again. */
    statusChangedBy: raw.status_changed_by,
  }));

export type Account = z.infer<typeof accountSchema>;

export const accountListSchema = z.object({
  items: z.array(accountSchema),
  total: z.number(),
});

/**
 * A role, as the server publishes it.
 *
 * Fetched rather than declared. Roles are rows in a table precisely so that
 * adding one is data; a list repeated here would be a second copy that nothing
 * keeps in step, and the day it drifts neither copy says so.
 */
export const roleSchema = z.object({ code: z.string(), name: z.string() });
export const roleListSchema = z.object({ items: z.array(roleSchema) });

export type Role = z.infer<typeof roleSchema>;

/**
 * The one-time password handed back when an account is created or reset.
 *
 * It arrives exactly once and is never retrievable again — a lost one is a
 * reset — so the screen that receives it has to show it until somebody
 * dismisses it rather than flashing it in a toast.
 */
export const createdAccountSchema = z.object({
  user: accountSchema,
  one_time_password: z.string(),
});

export const passwordResetSchema = z.object({ one_time_password: z.string() });

/**
 * One account a bulk operation declined to touch.
 *
 * `reason` is parsed as a bare string, not `z.enum(SKIP_REASONS)`. The list in
 * `accounts-terms.ts` is what this build knows how to word; a backend that has
 * shipped a newer reason must not have the row rejected or the field dropped
 * for saying something unfamiliar — the administrator still needs to see that
 * the account was skipped, even under a name the interface cannot translate
 * yet. The selection bar looks the value up in SKIP_REASONS and falls back to
 * showing it raw.
 */
export const skippedAccountSchema = z.object({
  id: z.string(),
  login: z.string(),
  reason: z.string(),
});

export type SkippedAccount = z.infer<typeof skippedAccountSchema>;

/**
 * What a status change or a role replacement did to a selection.
 *
 * Always 200, never a partial failure: the ids that changed and the ids that
 * were skipped, each with why. See `backend/internal/api/users_bulk_handler.go`
 * (`bulkResponse`) for the wire shape this mirrors.
 */
export const bulkResultSchema = z.object({
  changed: z.array(z.string()),
  skipped: z.array(skippedAccountSchema),
});

export type BulkResult = z.infer<typeof bulkResultSchema>;

/**
 * One password a bulk reset issued.
 *
 * Same handling as the single-account reset: it arrives exactly once, is
 * never stored in clear, and cannot be fetched again — a lost one is another
 * reset.
 */
export const issuedPasswordSchema = z
  .object({
    id: z.string(),
    login: z.string(),
    one_time_password: z.string(),
  })
  .transform((raw) => ({
    id: raw.id,
    login: raw.login,
    oneTimePassword: raw.one_time_password,
  }));

export type IssuedPassword = z.infer<typeof issuedPasswordSchema>;

/** What a bulk password reset did: the passwords issued, and the skips. */
export const bulkPasswordResetResultSchema = z.object({
  issued: z.array(issuedPasswordSchema),
  skipped: z.array(skippedAccountSchema),
});

export type BulkPasswordResetResult = z.infer<typeof bulkPasswordResetResultSchema>;

/**
 * Soft-deletes one account. The server refuses an empty reason
 * (`users.ErrReasonRequired`) and refuses deleting the last administrator or
 * yourself the same way the single-account block does.
 */
export async function deleteAccount(userId: string, reason: string): Promise<void> {
  await serverRequest(`/users/${userId}/delete`, { method: "POST", body: { reason } });
}

/** Reverses a soft delete. No body: there is nothing else to say about it. */
export async function restoreAccount(userId: string): Promise<void> {
  await serverRequest(`/users/${userId}/restore`, { method: "POST" });
}

/**
 * Moves every account in a selection to one status, in a single request.
 *
 * `status` is typed to `AccountStatus` rather than a bare string so a caller
 * cannot send a status this build does not itself understand — the server
 * would refuse it anyway, but the point of the type is to say so before the
 * request leaves.
 */
export async function bulkSetStatus(
  ids: string[],
  status: AccountStatus,
  reason: string,
): Promise<BulkResult> {
  const payload = await serverRequest("/users/bulk/status", {
    method: "POST",
    body: { ids, status, reason },
  });
  return bulkResultSchema.parse(payload);
}

/**
 * Replaces the role set on every account in a selection. Replace, not merge —
 * the same semantics as the single-account `PUT /roles`.
 */
export async function bulkReplaceRoles(ids: string[], roles: string[]): Promise<BulkResult> {
  const payload = await serverRequest("/users/bulk/roles", {
    method: "POST",
    body: { ids, roles },
  });
  return bulkResultSchema.parse(payload);
}

/** Issues a fresh one-time password for every account in a selection. */
export async function bulkResetPassword(ids: string[]): Promise<BulkPasswordResetResult> {
  const payload = await serverRequest("/users/bulk/password-reset", {
    method: "POST",
    body: { ids },
  });
  return bulkPasswordResetResultSchema.parse(payload);
}

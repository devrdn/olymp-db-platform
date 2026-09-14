import { z } from "zod";

import { ACCOUNT_STATUSES, type AccountStatus } from "./accounts-terms";
import { serverRequest } from "./server";

export {
  ACCOUNT_STATUSES,
  type AccountStatus,
  SKIP_REASONS,
  type SkipReason,
  MAX_BULK_ACCOUNTS,
  IMPORT_SKIP_REASONS,
  type ImportSkipReason,
  MAX_IMPORT_ROWS,
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
    // All four empty for an account nobody has ever blocked or deleted — see
    // `UserResponse` in `backend/internal/api/users_handler.go`.
    status_reason: z.string().optional(),
    status_changed_at: z.string().optional(),
    status_changed_by: z.string().optional(),
    // The actor's login, resolved by the repository's own query (a LEFT JOIN,
    // not a second `GET /users/{id}`) — see `internal/postgres/users.go`.
    status_changed_by_login: z.string().optional(),
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
    /** The changing actor's id. */
    statusChangedBy: raw.status_changed_by,
    /**
     * The changing actor's login, already resolved by the server's own query
     * — no second request needed to name them. "" when statusChangedBy is,
     * or (rarely) when the actor's id names no account.
     */
    statusChangedByLogin: raw.status_changed_by_login ?? "",
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

export type CreatedAccount = z.infer<typeof createdAccountSchema>;

export const passwordResetSchema = z.object({ one_time_password: z.string() });

/**
 * One row of a roster an import declined to create.
 *
 * No `id`, unlike `skippedAccountSchema` below: that one names an account a
 * bulk operation found and chose not to touch, and this one never became an
 * account at all — the login it was given on the roster is the only thing
 * left to find the line by.
 */
export const importSkippedRowSchema = z.object({ login: z.string(), reason: z.string() });

export type ImportSkippedRow = z.infer<typeof importSkippedRowSchema>;

/**
 * What importing a roster did: every account it created, one-time password
 * included, and every row it could not use, with why.
 *
 * Partial success, the same honesty `bulkResultSchema` reports: one taken
 * login must not cost the rest of the roster the accounts they could have
 * had, and whoever pasted the list has to see which line to fix. An import
 * stopped partway also names the rows it never reached (`not_imported`) and
 * why (`stopped`, an error code).
 */
export const importResultSchema = z.object({
  created: z.array(createdAccountSchema),
  skipped: z.array(importSkippedRowSchema),
  // An import the server's password hashing was too busy to finish is still
  // answered with what it created — those passwords exist nowhere else — plus
  // the logins it never reached and the error code saying why. Optional so a
  // server that predates them still parses.
  not_imported: z.array(z.string()).optional(),
  stopped: z.string().optional(),
});

export type ImportResult = z.infer<typeof importResultSchema>;

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
 * Registers one account.
 *
 * The server generates the password — an administrator never chooses one
 * that outlives the handover — and returns it exactly once, here, in
 * `CreatedAccount.one_time_password`. Not stored in clear and not
 * retrievable again: a lost one is a reset, the same as every other one-time
 * password this screen issues.
 */
export async function createAccount(cmd: {
  login: string;
  fullName: string;
  email: string;
  roles: string[];
}): Promise<CreatedAccount> {
  const payload = await serverRequest("/users", {
    method: "POST",
    body: { login: cmd.login, full_name: cmd.fullName, email: cmd.email, roles: cmd.roles },
  });
  return createdAccountSchema.parse(payload);
}

/**
 * Registers a whole roster at once — a department's list rather than one
 * request per person, thirty round trips and thirty chances to lose one.
 *
 * Every account created gets the same generated, one-time password
 * `createAccount` issues; every row that could not be used comes back with
 * why, from the closed vocabulary `IMPORT_SKIP_REASONS` names.
 */
export async function importAccounts(
  rows: { login: string; fullName: string; email: string }[],
  roles: string[],
): Promise<ImportResult> {
  const payload = await serverRequest("/users/import", {
    method: "POST",
    body: {
      rows: rows.map((row) => ({ login: row.login, full_name: row.fullName, email: row.email })),
      roles,
    },
  });
  return importResultSchema.parse(payload);
}

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

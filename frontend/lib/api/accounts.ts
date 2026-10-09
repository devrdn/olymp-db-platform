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
 * The wire shapes of an account and the roles it may hold, and the account
 * administration requests. snake_case is mapped to camelCase once, here.
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
    // All four empty for an account nobody has ever blocked or deleted.
    status_reason: z.string().optional(),
    status_changed_at: z.string().optional(),
    status_changed_by: z.string().optional(),
    status_changed_by_login: z.string().optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    login: raw.login,
    email: raw.email,
    fullName: raw.full_name,
    status: raw.status,
    /** Role codes; names come from the role catalogue. */
    roles: raw.roles,
    /** Still on the password an administrator handed over. */
    mustChangePassword: raw.must_change_password,
    lastLoginAt: raw.last_login_at,
    createdAt: raw.created_at,
    statusReason: raw.status_reason ?? "",
    statusChangedAt: raw.status_changed_at,
    /** The changing actor's id. */
    statusChangedBy: raw.status_changed_by,
    /** Resolved by the server; "" when unknown or the actor's id names no account. */
    statusChangedByLogin: raw.status_changed_by_login ?? "",
  }));

export type Account = z.infer<typeof accountSchema>;

export const accountListSchema = z.object({
  items: z.array(accountSchema),
  total: z.number(),
});

/** A role, fetched rather than declared: roles are data, and a copy here would drift. */
export const roleSchema = z.object({ code: z.string(), name: z.string() });
export const roleListSchema = z.object({ items: z.array(roleSchema) });

export type Role = z.infer<typeof roleSchema>;

/**
 * A new account and its one-time password. The password arrives once and is
 * never retrievable, so show it until dismissed, not in a toast.
 */
export const createdAccountSchema = z.object({
  user: accountSchema,
  one_time_password: z.string(),
});

export type CreatedAccount = z.infer<typeof createdAccountSchema>;

export const passwordResetSchema = z.object({ one_time_password: z.string() });

/** A roster row an import declined. No `id`: it never became an account. */
export const importSkippedRowSchema = z.object({ login: z.string(), reason: z.string() });

export type ImportSkippedRow = z.infer<typeof importSkippedRowSchema>;

/**
 * What importing a roster did. Partial success: one taken login must not cost
 * the rest their accounts, and every unusable row comes back with why.
 */
export const importResultSchema = z.object({
  created: z.array(createdAccountSchema),
  skipped: z.array(importSkippedRowSchema),
  // An import stopped partway (password hashing too busy) still returns what
  // it created, since those passwords exist nowhere else, plus the logins it
  // never reached and the error code. Optional for older servers.
  not_imported: z.array(z.string()).optional(),
  stopped: z.string().optional(),
});

export type ImportResult = z.infer<typeof importResultSchema>;

/**
 * One account a bulk operation declined to touch. `reason` is a bare string,
 * not an enum, so a reason newer than this build still shows (raw) instead of
 * failing the parse.
 */
export const skippedAccountSchema = z.object({
  id: z.string(),
  login: z.string(),
  reason: z.string(),
});

export type SkippedAccount = z.infer<typeof skippedAccountSchema>;

/** What a bulk status or role change did: always 200, with each skip and why. */
export const bulkResultSchema = z.object({
  changed: z.array(z.string()),
  skipped: z.array(skippedAccountSchema),
});

export type BulkResult = z.infer<typeof bulkResultSchema>;

/** One password a bulk reset issued; like any one-time password, it arrives once. */
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

export const bulkPasswordResetResultSchema = z.object({
  issued: z.array(issuedPasswordSchema),
  skipped: z.array(skippedAccountSchema),
});

export type BulkPasswordResetResult = z.infer<typeof bulkPasswordResetResultSchema>;

/**
 * Registers one account. The server generates the password and returns it
 * only here; a lost one means a reset.
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
 * Registers a whole roster in one request. Each account gets a one-time
 * password; each unusable row comes back with an `IMPORT_SKIP_REASONS` reason.
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
 * Soft-deletes one account. The server refuses an empty reason, the last
 * administrator, and yourself.
 */
export async function deleteAccount(userId: string, reason: string): Promise<void> {
  await serverRequest(`/users/${userId}/delete`, { method: "POST", body: { reason } });
}

export async function restoreAccount(userId: string): Promise<void> {
  await serverRequest(`/users/${userId}/restore`, { method: "POST" });
}

/** Moves every account in a selection to one status, in a single request. */
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

/** Replaces (not merges) the role set on every account in a selection. */
export async function bulkReplaceRoles(ids: string[], roles: string[]): Promise<BulkResult> {
  const payload = await serverRequest("/users/bulk/roles", {
    method: "POST",
    body: { ids, roles },
  });
  return bulkResultSchema.parse(payload);
}

export async function bulkResetPassword(ids: string[]): Promise<BulkPasswordResetResult> {
  const payload = await serverRequest("/users/bulk/password-reset", {
    method: "POST",
    body: { ids },
  });
  return bulkPasswordResetResultSchema.parse(payload);
}

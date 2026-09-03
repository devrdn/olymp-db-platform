import { z } from "zod";

import { ACCOUNT_STATUSES } from "./accounts-terms";

export { ACCOUNT_STATUSES, type AccountStatus } from "./accounts-terms";

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

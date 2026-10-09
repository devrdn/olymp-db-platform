import { z } from "zod";

import { SQL_MODES } from "./policy-terms";

export { SQL_MODES } from "./policy-terms";


/**
 * The wire shape of a contest's SQL access policy, which the game loop turns
 * into GRANTs for a participant's database.
 */

/** Whether participants may only read or may also write. */
export type SqlMode = (typeof SQL_MODES)[number];

/**
 * An optionally schema-qualified lowercase identifier. Stricter than
 * PostgreSQL because these names go into GRANT statements unparameterised;
 * the server checks too, this only moves the error under the field.
 */
const TABLE_NAME = /^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$/;

export const sqlPolicySchema = z
  .object({
    mode: z.enum(SQL_MODES),
    writable_tables: z.array(z.string()),
    allow_create_view: z.boolean(),
    allow_own_tables: z.boolean(),
    allow_temp_tables: z.boolean(),
    allow_catalog: z.boolean(),
    disk_quota_ratio: z.number(),
    updated_at: z.string().optional(),
  })
  .transform((raw) => ({
    mode: raw.mode,
    writableTables: raw.writable_tables,
    allowCreateView: raw.allow_create_view,
    allowOwnTables: raw.allow_own_tables,
    allowTempTables: raw.allow_temp_tables,
    allowCatalog: raw.allow_catalog,
    diskQuotaRatio: raw.disk_quota_ratio,
    updatedAt: raw.updated_at,
  }));

export type SqlPolicy = z.infer<typeof sqlPolicySchema>;

export function isTableName(value: string): boolean {
  return TABLE_NAME.test(value);
}

/** Splits a pasted list of tables, reporting every unusable entry rather than the first. */
export function parseTables(pasted: string): { tables: string[]; rejected: string[] } {
  const tables: string[] = [];
  const rejected: string[] = [];

  for (const raw of pasted.split(/[\s,;]+/)) {
    const name = raw.trim();
    if (!name) continue;
    if (isTableName(name)) {
      if (!tables.includes(name)) tables.push(name);
    } else {
      rejected.push(name);
    }
  }

  return { tables, rejected };
}

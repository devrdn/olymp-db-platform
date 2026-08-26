import { z } from "zod";

/**
 * The wire shape of a contest's SQL access policy.
 *
 * Its own module because its consumer is not the constructor. The game loop
 * reads this policy to build the GRANTs for a participant's database, so it
 * travels separately from the contest's titles and schedule — which is the
 * same reason the Go side gives the policy a store of its own.
 */

export const SQL_MODES = ["read_only", "read_write"] as const;
export type SqlMode = (typeof SQL_MODES)[number];

/**
 * The shape a writable table name may take: an optionally schema-qualified
 * lowercase identifier.
 *
 * Stricter than PostgreSQL allows, and checked here as well as on the server.
 * These names become GRANT statements when the game template is built, where
 * they cannot be passed as parameters — the narrow form is what makes that
 * construction safe whatever an author types into the field. Checking early
 * turns a rejected save into a message under the field it belongs to.
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

/**
 * Splits a pasted list of tables and reports which entries are unusable.
 *
 * Both halves are returned rather than throwing on the first bad one: an
 * author pasting eight table names wants to be told about all the typos at
 * once, which is the same reason the publish gate returns every problem
 * instead of the first.
 */
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

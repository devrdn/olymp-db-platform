import { z } from "zod";

/**
 * A row of the participant's own query log (`.../play/log`). `status` is a
 * plain string so an unknown status still renders, as its raw code. `sql` is
 * only the start of the statement when `sql_truncated` says so, since a page
 * is bounded in bytes; the CSV export has it whole.
 */
export const queryLogEntrySchema = z
  .object({
    sql: z.string(),
    sql_truncated: z.boolean().nullish(),
    status: z.string(),
    error: z.string().optional(),
    duration_ms: z.number().nullish(),
    row_count: z.number().nullish(),
    executed_at: z.string(),
  })
  .transform((raw) => ({
    sql: raw.sql,
    // Omitted for a row the server did not cut.
    sqlTruncated: raw.sql_truncated ?? false,
    status: raw.status,
    error: raw.error ?? "",
    durationMs: raw.duration_ms ?? undefined,
    rowCount: raw.row_count ?? undefined,
    executedAt: raw.executed_at,
  }));

export type QueryLogEntry = z.infer<typeof queryLogEntrySchema>;

export const queryLogResponseSchema = z.object({
  items: z.array(queryLogEntrySchema),
  total: z.number(),
});

export type QueryLogResponse = z.infer<typeof queryLogResponseSchema>;

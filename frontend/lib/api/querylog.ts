import { z } from "zod";

/**
 * The wire shape of a participant's own query log — GET .../play/log.
 *
 * A row's status is a free-form server string (queryrunner.Status is a Go
 * string type, not an enum a client generator narrowed), so it is kept as
 * `string` here rather than a closed union: a status this build has no
 * wording for still has to render as something (the log panel falls back to
 * the raw code), the same way an unrecognised error code already does
 * elsewhere in this interface.
 */
export const queryLogEntrySchema = z
  .object({
    sql: z.string(),
    status: z.string(),
    error: z.string().optional(),
    duration_ms: z.number().nullish(),
    row_count: z.number().nullish(),
    executed_at: z.string(),
  })
  .transform((raw) => ({
    sql: raw.sql,
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

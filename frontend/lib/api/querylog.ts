import { z } from "zod";

/**
 * The wire shape of a participant's own query log — GET .../play/log, and
 * of one entry of it in full — GET .../play/log/{entryId} (§7). Both read
 * the same row through the same field names, so one schema serves both:
 * the single-entry response is exactly this shape with `sql` never cut.
 *
 * A row's status is a free-form server string (queryrunner.Status is a Go
 * string type, not an enum a client generator narrowed), so it is kept as
 * `string` here rather than a closed union: a status this build has no
 * wording for still has to render as something (the log panel falls back to
 * the raw code), the same way an unrecognised error code already does
 * elsewhere in this interface.
 *
 * `sql` is the beginning of the statement rather than the whole of it
 * whenever `sql_truncated` says so: one page is bounded in bytes as well as
 * in rows, because two hundred rows of a 64 KiB statement each would
 * otherwise be a twelve-megabyte response. The whole statement is always in
 * the CSV export beside the panel, and in the single-entry response above.
 *
 * `id` is what a client names back at GET .../play/log/{entryId} to open a
 * row the page cut. Required rather than optional: every row the server
 * sends carries it, on both routes.
 */
export const queryLogEntrySchema = z
  .object({
    id: z.string(),
    sql: z.string(),
    sql_truncated: z.boolean().nullish(),
    status: z.string(),
    error: z.string().optional(),
    duration_ms: z.number().nullish(),
    row_count: z.number().nullish(),
    executed_at: z.string(),
  })
  .transform((raw) => ({
    id: raw.id,
    sql: raw.sql,
    // Omitted by the server for a row it did not cut, so absent means whole.
    // Normalised to a boolean here rather than left as `undefined` for the
    // panel to guard against: whether the statement was cut is a fact about
    // every row, not a field only some rows have.
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

/**
 * GET .../play/log/{entryId}'s own response: one row, in full — the same
 * shape queryLogEntrySchema already parses, so fetching one entry needs no
 * schema of its own, only this name for the endpoint that returns it.
 */
export const queryLogEntryDetailSchema = queryLogEntrySchema;

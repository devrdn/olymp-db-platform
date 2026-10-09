import { z } from "zod";

/**
 * What comes back from running one query. A cell is the database's value as
 * text, or null; null stays distinct from "" because SQL distinguishes them.
 */
export const queryResultSchema = z.object({
  columns: z.array(z.string()).default([]),
  /**
   * Each column's type as PostgreSQL prints it, the nth under the nth column.
   * Either empty or as long as `columns`; an entry is "" for a type the runner
   * could not name. Optional rather than defaulted: a result without types
   * (an older runner, a query that never reached a database) still renders.
   */
  column_types: z.array(z.string()).optional(),
  rows: z.array(z.array(z.union([z.string(), z.null()]))).default([]),
  /** Cut by rows, by bytes, or both. */
  truncated: z.boolean().default(false),
  /** Zero for a read. */
  rows_affected: z.number().default(0),
  /**
   * The statement alone, in microseconds, so quick queries do not round to
   * zero before display. Absent means never timed, distinct from zero.
   */
  duration_micros: z.number().optional(),
});

export type QueryResult = z.infer<typeof queryResultSchema>;

import { z } from "zod";

/**
 * What comes back from running one query.
 *
 * A cell is whatever the database had in it, rendered as text by the Query
 * Runner, or null. Null is carried as null rather than as an empty string
 * because in SQL those are different things, and a participant debugging a
 * left join is looking at exactly that column.
 */
export const queryResultSchema = z.object({
  columns: z.array(z.string()).default([]),
  rows: z.array(z.array(z.union([z.string(), z.null()]))).default([]),
  /** The answer is longer than what is here — by rows, by bytes, or both. */
  truncated: z.boolean().default(false),
  /** How many rows a write changed. Zero for a read. */
  rows_affected: z.number().default(0),
});

export type QueryResult = z.infer<typeof queryResultSchema>;

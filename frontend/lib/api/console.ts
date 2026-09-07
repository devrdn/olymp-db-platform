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
  /**
   * Each column's type, in the spelling PostgreSQL itself prints — `text`,
   * `timestamp with time zone`, `uuid`. The same vocabulary the schema panel
   * shows one pane to the left, so that the console names a type the same way
   * twice on one screen.
   *
   * A list beside `columns` rather than a list of objects in place of it: the
   * results table and the CSV export both read `columns` as strings, and a
   * label under a heading is not worth changing the shape every one of them
   * takes.
   *
   * Read together with `columns` — the nth type belongs under the nth name.
   * Either empty, or exactly as long as `columns`; an individual entry is
   * empty for a type the runner could not name, which is a blank to leave
   * rather than a hole to close up.
   *
   * Optional where its neighbours carry a default, and the difference is not
   * an oversight. `columns` and `rows` are the answer, and an answer that
   * arrived without one of them is an answer this client cannot draw; a type
   * is a label above the answer, and the console has to render a result that
   * has none — from a runner older than this field, or from a query that
   * never reached a database. `column_types?.[i] ?? ""` is the reading, and
   * the absent case is a real one rather than a shape to normalise away.
   * `null` is still refused: the API promises a list or nothing.
   */
  column_types: z.array(z.string()).optional(),
  rows: z.array(z.array(z.union([z.string(), z.null()]))).default([]),
  /** The answer is longer than what is here — by rows, by bytes, or both. */
  truncated: z.boolean().default(false),
  /** How many rows a write changed. Zero for a read. */
  rows_affected: z.number().default(0),
  /**
   * How long the statement itself took, in microseconds — the meter under the
   * editor, which shows it rounded to milliseconds.
   *
   * Microseconds on the wire precisely so that the rounding happens here: a
   * duration already rounded to milliseconds by the API would make every quick
   * query read as zero. The statement and nothing around it — not the
   * connection, not the queue, not the request — because those are the
   * platform's costs and not the participant's query's.
   *
   * Absent means the statement was never timed — an older runner, or an
   * answer that did not come from one. Kept distinct from a measured zero
   * rather than defaulted, so the meter can leave the field blank instead of
   * claiming a query took no time at all.
   */
  duration_micros: z.number().optional(),
});

export type QueryResult = z.infer<typeof queryResultSchema>;

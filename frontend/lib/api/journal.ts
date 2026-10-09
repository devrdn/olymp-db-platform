import { z } from "zod";

/**
 * The shapes of one participant's record (every query and answer), shared by
 * staff (`./monitor`) and the participant (`./profile`). The server builds
 * both from one query, the participant's copy minus the address. Paths and
 * reads stay with each audience. The live log of the play screen is
 * `./querylog`.
 */

/** Also the only values a queries filter takes (`monitor.queryStatuses`). */
export const QUERY_STATUSES = ["running", "ok", "error", "rejected", "timeout"] as const;

/** The most rows one page of queries carries (`monitor.MaxQueriesPage`). */
export const MAX_QUERIES_PAGE = 50;

/** The most characters a query search may hold (`monitor.MaxQuerySearchRunes`). */
export const MAX_QUERY_SEARCH = 200;

/**
 * One logged query. `ip` is absent on rows logged before addresses were
 * kept, and always absent from the participant's copy.
 */
export const loggedQuerySchema = z
  .object({
    cursor: z.string(),
    executed_at: z.string(),
    id: z.number(),
    sql: z.string(),
    sql_truncated: z.boolean().optional(),
    status: z.string(),
    error: z.string().optional(),
    duration_ms: z.number().nullable().optional(),
    row_count: z.number().nullable().optional(),
    ip: z.string().optional(),
  })
  .transform((raw) => ({
    cursor: raw.cursor,
    executedAt: raw.executed_at,
    id: raw.id,
    sql: raw.sql,
    sqlTruncated: raw.sql_truncated ?? false,
    status: raw.status,
    error: raw.error,
    durationMs: raw.duration_ms ?? null,
    rowCount: raw.row_count ?? null,
    ip: raw.ip,
  }));

export type LoggedQuery = z.infer<typeof loggedQuerySchema>;

/** Newest first; `more` says there is an older page. */
export const queriesSchema = z.object({ items: z.array(loggedQuerySchema), more: z.boolean() });

export type QueriesPage = z.infer<typeof queriesSchema>;

export type QueriesParams = { status?: string; q?: string; cursor?: string; limit?: number };

export type ReadOptions = { signal?: AbortSignal };

const attemptSchema = z
  .object({
    id: z.string(),
    question_ord: z.number(),
    attempt_no: z.number(),
    value: z.string(),
    correct: z.boolean(),
    points_awarded: z.number(),
    submitted_at: z.string(),
    queries: z.array(loggedQuerySchema),
    more_queries: z.number(),
  })
  .transform((raw) => ({
    id: raw.id,
    questionOrd: raw.question_ord,
    attemptNo: raw.attempt_no,
    value: raw.value,
    correct: raw.correct,
    points: raw.points_awarded,
    submittedAt: raw.submitted_at,
    /** The queries that led to the attempt, oldest first. */
    queries: raw.queries,
    /** How many more led to it than `queries` carries. */
    moreQueries: raw.more_queries,
  }));

export type Attempt = z.infer<typeof attemptSchema>;

/** Every attempt, by question in order. */
export const answersSchema = z
  .object({
    questions: z.array(
      z
        .object({ question_id: z.string(), question_ord: z.number(), attempts: z.array(attemptSchema) })
        .transform((raw) => ({ questionId: raw.question_id, questionOrd: raw.question_ord, attempts: raw.attempts })),
    ),
    truncated: z.boolean(),
  })
  .transform((raw) => ({ questions: raw.questions, truncated: raw.truncated }));

export type Answers = z.infer<typeof answersSchema>;

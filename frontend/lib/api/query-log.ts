import { z } from "zod";

/**
 * The shapes of a participant's SQL log and answers, as two audiences read
 * them.
 *
 * A contest's staff read them under `/contests/{id}/monitor/participants/{id}/…`
 * (`./monitor`); the participant reads their own under `/me/contests/{id}/…`
 * (`./profile`). The server serves one record from one query — the participant's
 * copy is the organiser's minus the address, with the error text the play
 * screen would have shown — so the reader is one file rather than two that
 * drift the first time a field is added.
 *
 * Only the shapes live here. Each audience keeps its own paths and its own
 * browser-side reads, because those are what the two differ in: a route, a
 * permission and a refusal.
 */

/** Every status a query may have, and the only ones a queries filter takes (monitor.queryStatuses). */
export const QUERY_STATUSES = ["running", "ok", "error", "rejected", "timeout"] as const;

/** The most queries one page carries (monitor.MaxQueriesPage). */
export const MAX_QUERIES_PAGE = 50;

/** The longest text a queries search takes, in characters (monitor.MaxQuerySearchRunes). */
export const MAX_QUERY_SEARCH = 200;

/**
 * One logged query, whole.
 *
 * `ip` is optional because two things leave it out: a row journalled before
 * addresses were kept, and the participant's own copy of the log, which never
 * carries one (design §2.2).
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

/** One page of queries, newest first; `more` says there is an older page. */
export const queriesSchema = z.object({ items: z.array(loggedQuerySchema), more: z.boolean() });

export type QueriesPage = z.infer<typeof queriesSchema>;

/** What a queries read may ask: a status, a substring, and where to carry on from. */
export type QueriesParams = { status?: string; q?: string; cursor?: string; limit?: number };

/** What a browser-side read may be called off with. */
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
    /** The queries that led to the attempt, oldest first (design §3). */
    queries: raw.queries,
    /** How many more queries led to it than `queries` carries. */
    moreQueries: raw.more_queries,
  }));

export type Attempt = z.infer<typeof attemptSchema>;

/** Every attempt, by question in order, each with the queries that led to it. */
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

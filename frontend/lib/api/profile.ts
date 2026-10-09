import { z } from "zod";

import { API_PREFIX, request } from "./client";
import { CONTEST_STATUSES } from "./contests";
import { SCORINGS } from "./contests-terms";
import { queriesSchema, type QueriesPage, type QueriesParams, type ReadOptions } from "./journal";

/**
 * The wire shapes of a participant's own profile and contest reports (`/me/…`,
 * docs/ARCHITECTURE.md §9.5). A report's queries use the shapes of `./journal`,
 * which the monitoring routes share.
 */

/**
 * The table's state, as `leaderboard.Decide` names it. `not_started` is
 * reachable: a participant disqualified from a published contest is finished
 * with it, so their report carries a result of a table that never opened.
 */
export const TABLE_STATES = ["not_started", "live", "frozen", "final"] as const;
export type TableState = (typeof TABLE_STATES)[number];

export const profileSummarySchema = z.object({
  contests: z.number(),
  finished: z.number(),
  queries: z.number(),
  solved: z.number(),
});

export type ProfileSummary = z.infer<typeof profileSummarySchema>;

/**
 * What the participant scored. `penalty` is sent only under ICPC scoring,
 * where `points` is always zero. No place here: naming one would compute a
 * standings table per contest for an overview (docs/ARCHITECTURE.md §9.5);
 * `placeOpen` says whether the report can show one.
 */
export const profileResultSchema = z
  .object({
    scoring: z.enum(SCORINGS),
    points: z.number(),
    solved: z.number(),
    penalty: z.number().optional(),
    state: z.enum(TABLE_STATES),
    place_open: z.boolean(),
  })
  .transform((raw) => ({
    scoring: raw.scoring,
    points: raw.points,
    solved: raw.solved,
    penalty: raw.penalty,
    state: raw.state,
    placeOpen: raw.place_open,
  }));

export type ProfileResult = z.infer<typeof profileResultSchema>;

export const profileContestSchema = z
  .object({
    contest_id: z.string(),
    title: z.string(),
    status: z.enum(CONTEST_STATUSES),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    /** The caller's standing on the roster: registered, active, finished, disqualified. */
    registration_status: z.string(),
    /** Whether the contest has ended for this caller, and so whether its report opens. */
    over: z.boolean(),
    /**
     * Absent, not zeroed, until the contest is over for the caller: "nothing
     * yet" must differ from a result of nought.
     */
    result: profileResultSchema.optional(),
  })
  .transform((raw) => ({
    contestId: raw.contest_id,
    title: raw.title,
    status: raw.status,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    registrationStatus: raw.registration_status,
    over: raw.over,
    result: raw.result,
  }));

export type ProfileContest = z.infer<typeof profileContestSchema>;

export const profileContestsSchema = z.object({
  items: z.array(profileContestSchema),
  /** The account is on more contests than one profile carries. */
  truncated: z.boolean(),
});

export type ProfileContests = z.infer<typeof profileContestsSchema>;

// The report of one finished contest: four tabs, each its own read. Any
// contest not ended for this participant is the same 404, so the reader
// never learns which case it was.

/** What the participant scored, with the place a single contest can afford to name. */
const reportResultSchema = z
  .object({
    scoring: z.enum(SCORINGS),
    points: z.number(),
    solved: z.number(),
    penalty: z.number().optional(),
    state: z.enum(TABLE_STATES),
    place_open: z.boolean(),
    /**
     * Null, not zero, when the table is not open yet or places only its
     * winner. `participants` travels with it.
     */
    place: z.number().nullish(),
    participants: z.number().nullish(),
    /** The one registration that won a winner-mode contest. */
    winner: z.boolean().optional(),
    /** The table was cut at the leaderboard's bound, so `participants` counts its rows. */
    truncated: z.boolean().optional(),
  })
  .transform((raw) => ({
    scoring: raw.scoring,
    points: raw.points,
    solved: raw.solved,
    penalty: raw.penalty,
    state: raw.state,
    placeOpen: raw.place_open,
    place: raw.place ?? null,
    participants: raw.participants ?? null,
    winner: raw.winner ?? false,
    truncated: raw.truncated ?? false,
  }));

export type ProfileReportResult = z.infer<typeof reportResultSchema>;

const reportQuestionSchema = z
  .object({
    question_id: z.string(),
    ord: z.number(),
    attempts: z.number(),
    solved: z.boolean(),
    solved_at: z.string().optional(),
    points: z.number(),
    penalty: z.number().optional(),
  })
  .transform((raw) => ({
    questionId: raw.question_id,
    ord: raw.ord,
    attempts: raw.attempts,
    solved: raw.solved,
    solvedAt: raw.solved_at,
    /** Always zero under ICPC scoring. */
    points: raw.points,
    /**
     * ICPC only, in minutes: the solving minute plus the penalty for each
     * wrong attempt before it. Zero in other modes.
     */
    penalty: raw.penalty ?? 0,
  }));

export type ProfileReportQuestion = z.infer<typeof reportQuestionSchema>;

export const profileReportSchema = z
  .object({
    contest_id: z.string(),
    title: z.string(),
    status: z.enum(CONTEST_STATUSES),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    /**
     * Null, not zeroed, for a row the published table does not carry (below
     * its row bound, or disqualified before it was computed). The rest of the
     * report still arrives.
     */
    result: reportResultSchema.nullable(),
    started_at: z.string().optional(),
    queries: z.number(),
    successful_queries: z.number(),
    /** From the clock starting to the last answer; absent if either is missing. */
    worked_ms: z.number().optional(),
    /** Never says why: the reason lives in the audit trail. */
    disqualified: z.boolean().optional(),
    questions: z.array(reportQuestionSchema),
    /** More attempts were made than one read of the answers carries. */
    truncated: z.boolean().optional(),
  })
  .transform((raw) => ({
    contestId: raw.contest_id,
    title: raw.title,
    status: raw.status,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    result: raw.result,
    startedAt: raw.started_at,
    queries: raw.queries,
    successfulQueries: raw.successful_queries,
    workedMs: raw.worked_ms,
    disqualified: raw.disqualified ?? false,
    questions: raw.questions,
    truncated: raw.truncated ?? false,
  }));

export type ProfileReport = z.infer<typeof profileReportSchema>;

/**
 * The notes and SQL tabs as the contest left them. No revisions: edit history
 * is the organiser's monitoring tool.
 */
export const profileWorkspaceSchema = z
  .object({
    notes: z.object({ body: z.string(), updated_at: z.string().nullish() }),
    tabs: z.array(
      z.object({ id: z.string(), title: z.string(), position: z.number(), body: z.string(), updated_at: z.string() }),
    ),
  })
  .transform((raw) => ({
    notes: { body: raw.notes.body, updatedAt: raw.notes.updated_at ?? null },
    tabs: raw.tabs.map((tab) => ({
      id: tab.id,
      title: tab.title,
      position: tab.position,
      body: tab.body,
      updatedAt: tab.updated_at,
    })),
  }));

export type ProfileWorkspace = z.infer<typeof profileWorkspaceSchema>;

function myContestBase(contestId: string): string {
  return `/me/contests/${encodeURIComponent(contestId)}`;
}

export function myQueriesPath(contestId: string, params: QueriesParams): string {
  const query = new URLSearchParams();
  if (params.status) query.set("status", params.status);
  if (params.q) query.set("q", params.q);
  if (params.cursor) query.set("cursor", params.cursor);
  if (params.limit) query.set("limit", String(params.limit));
  const text = query.toString();
  return text ? `${myContestBase(contestId)}/queries?${text}` : `${myContestBase(contestId)}/queries`;
}

/** Browser-side read of the caller's own queries. */
export async function fetchMyQueries(
  contestId: string,
  params: QueriesParams,
  options: ReadOptions = {},
): Promise<QueriesPage> {
  const payload = await request(myQueriesPath(contestId, params), {
    credentials: "same-origin",
    signal: options.signal,
  });
  return queriesSchema.parse(payload);
}

/**
 * The caller's log as a download link. A server address, not a blob built in
 * the page, which holds only a bounded slice of the log.
 */
export function myCsvHref(contestId: string): string {
  return `${API_PREFIX}${myContestBase(contestId)}/log.csv`;
}

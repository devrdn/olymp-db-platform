import { z } from "zod";

import { API_PREFIX, request } from "./client";
import { CONTEST_STATUSES } from "./contests";
import { SCORINGS } from "./contests-terms";
import { queriesSchema, type QueriesPage, type QueriesParams, type ReadOptions } from "./journal";

/**
 * The wire shapes of a participant's own profile
 * (`/me/…`, docs/superpowers/specs/2026-09-21-participant-profile-design.md §3).
 *
 * Read on the server, where the session already is, and parsed at the boundary
 * so a contract change surfaces here with the field name in the message rather
 * than as `undefined` three components later. The API speaks snake_case; the
 * interface speaks camelCase, and the mapping happens once, here.
 *
 * The profile screen's own two reads first, then the report's four. The
 * queries and the answers of a report are the shapes of `./journal`, which
 * the monitoring routes serve too: one record, one reader.
 */

/**
 * The table's own state, as `leaderboard.Decide` names it.
 *
 * `not_started` is here because it reaches a real reader, not for symmetry: a
 * participant disqualified from a published contest is finished with it
 * (`profile.Over`), so their row and their report carry a result — of a table
 * whose contest never opened. The server says so rather than rounding the
 * state up to a running one, and the screen has a sentence of its own for it.
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
 * What the participant scored, in whichever of the two shapes the contest's
 * mode makes a result.
 *
 * `penalty` is ICPC's and is sent in no other mode, which is what makes it the
 * honest discriminator: in ICPC scoring `points` is always zero — the server
 * writes none — so a row that read points there would report nought for four
 * solved questions.
 *
 * There is deliberately no place. Naming one means computing a whole standings
 * table per contest to decorate an overview (design §2.1); `placeOpen` says
 * only whether the report has a place to show, which is true of a final table
 * or a freeze an organiser revealed.
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
    /** The caller's own standing on the roster: registered, active, finished, disqualified. */
    registration_status: z.string(),
    /** Whether the contest has ended for this caller, and so whether its report opens. */
    over: z.boolean(),
    /**
     * Absent for a contest that is not over for the caller. Absent rather than
     * zeroed on purpose: a row has to tell "nothing to show yet" from "a
     * result of nought", and during a contest the profile shows nothing of
     * what is happening inside it (design §1).
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

/**
 * The report of one finished contest — the four tabs of design §2.2, each a
 * read of its own.
 *
 * Only a contest that has ended for this participant has one. Anything else —
 * somebody else's, one still running, one that does not exist — is the same
 * 404 from the API and the same not-found page here, so the screen never has
 * to tell them apart and never tells a reader which of the three it was.
 */

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
     * Null in two cases, and the interface says something different for each:
     * the table is not open yet, or it is open and places nobody but its
     * winner. A place of nought would read as a place, which is why neither
     * is zeroed. `participants` travels with it — a place is a place among a
     * number of people, and half of that pair says nothing.
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
    /** What the question earned. Always nought in ICPC scoring, which awards none. */
    points: raw.points,
    /**
     * What the question cost, in minutes: its solving minute plus the
     * contest's penalty for each wrong attempt before the solve. ICPC's, and
     * nought in every other mode, where nothing charges minutes.
     * `result.scoring` says which of the two a reader is shown.
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
     * Null for a row the published table does not carry — a participant below
     * the table's row bound, or one disqualified before it was computed. Null
     * rather than an object of zeroes on purpose: a zeroed one would name no
     * scoring mode and no table state, and a reader shown it is told a result
     * of nought under rules nobody can name. The rest of the report is still
     * the participant's own work and still arrives.
     */
    result: reportResultSchema.nullable(),
    started_at: z.string().optional(),
    queries: z.number(),
    successful_queries: z.number(),
    /**
     * From the clock starting to the last answer. Absent when either end is
     * missing: somebody who never started, or never answered, worked for no
     * stretch this can name.
     */
    worked_ms: z.number().optional(),
    /**
     * That the registration was excluded, and not a word about why. The
     * reason is the organiser's note in the audit trail, which has access
     * rules of its own (design §1).
     */
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
 * The notes and SQL tabs as the contest left them.
 *
 * No revisions: the history of an edit is a monitoring fact about how
 * somebody worked, and it is the organiser's tool rather than a record the
 * participant is handed back (design §2.2). The API sends none.
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

/** One finished contest of the caller's own: `/me/contests/{id}/…`. */
function myContestBase(contestId: string): string {
  return `/me/contests/${encodeURIComponent(contestId)}`;
}

/** The caller's own queries, with the filters given and no parameter for the rest. */
export function myQueriesPath(contestId: string, params: QueriesParams): string {
  const query = new URLSearchParams();
  if (params.status) query.set("status", params.status);
  if (params.q) query.set("q", params.q);
  if (params.cursor) query.set("cursor", params.cursor);
  if (params.limit) query.set("limit", String(params.limit));
  const text = query.toString();
  return text ? `${myContestBase(contestId)}/queries?${text}` : `${myContestBase(contestId)}/queries`;
}

/** GET /me/contests/{id}/queries, from the browser. */
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
 * The caller's own log as a file, as a link the browser downloads.
 *
 * An address on this origin, never a blob built in the page: what leaves the
 * server is decided by the server, and the page holds one bounded slice of
 * the log anyway (see ExportMenu's own doc).
 */
export function myCsvHref(contestId: string): string {
  return `${API_PREFIX}${myContestBase(contestId)}/log.csv`;
}

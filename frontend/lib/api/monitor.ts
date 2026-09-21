import { z } from "zod";

import { API_PREFIX, request } from "./client";
import {
  answersSchema,
  loggedQuerySchema,
  queriesSchema,
  type QueriesParams,
  type ReadOptions,
} from "./query-log";

/**
 * The wire shapes of the organiser's monitoring routes
 * (`/contests/{id}/monitor/…`, docs/superpowers/specs/2026-09-18-participant-monitoring-design.md §4)
 * and the browser-side reads the live screen polls them with.
 *
 * The live screen reads from the browser rather than through a Server Action:
 * a refusal's `Retry-After` is what the screen waits on after a 429, and a
 * Server Action hands back the error code and loses the header.
 */

/** Every kind the feed's `kinds` filter may name (monitor.FeedKinds). */
export const FEED_KINDS = [
  "answer",
  "disqualified",
  "finished",
  "ip_changed",
  "page_left",
  "parallel_session",
  "paste",
  "query",
  "sign_in",
  "sign_in_failed",
  "sign_out",
  "started",
  "tab_created",
  "tab_deleted",
  "tab_renamed",
] as const;
export type FeedKind = (typeof FEED_KINDS)[number];

/** The six flags of design §5, in the order the table shows them. */
export const MONITOR_FLAGS = [
  "multipleIps",
  "parallelSessions",
  "longAbsence",
  "answerWithoutQueries",
  "largePaste",
  "identicalQueries",
] as const;
export type MonitorFlag = (typeof MONITOR_FLAGS)[number];
export type MonitorFlags = Record<MonitorFlag, boolean>;

const flagsSchema = z
  .object({
    multiple_ips: z.boolean(),
    parallel_sessions: z.boolean(),
    long_absence: z.boolean(),
    answer_without_queries: z.boolean(),
    large_paste: z.boolean(),
    identical_queries: z.boolean(),
  })
  .transform(
    (raw): MonitorFlags => ({
      multipleIps: raw.multiple_ips,
      parallelSessions: raw.parallel_sessions,
      longAbsence: raw.long_absence,
      answerWithoutQueries: raw.answer_without_queries,
      largePaste: raw.large_paste,
      identicalQueries: raw.identical_queries,
    }),
  );

export const rosterRowSchema = z
  .object({
    registration_id: z.string(),
    login: z.string(),
    full_name: z.string(),
    status: z.string(),
    started_at: z.string().nullable(),
    finished_at: z.string().nullable(),
    queries: z.number(),
    query_errors: z.number(),
    query_rejected: z.number(),
    addresses: z.number(),
    correct: z.number(),
    wrong: z.number(),
    page_left: z.number(),
    away_ms: z.number(),
    pastes: z.number(),
    ip_changes: z.number(),
    parallel_sessions: z.number(),
    last_activity: z.string().nullable(),
    flags: flagsSchema,
  })
  .transform((raw) => ({
    registrationId: raw.registration_id,
    login: raw.login,
    fullName: raw.full_name,
    status: raw.status,
    startedAt: raw.started_at,
    finishedAt: raw.finished_at,
    queries: raw.queries,
    queryErrors: raw.query_errors,
    queryRejected: raw.query_rejected,
    addresses: raw.addresses,
    correct: raw.correct,
    wrong: raw.wrong,
    pageLeft: raw.page_left,
    awayMs: raw.away_ms,
    pastes: raw.pastes,
    ipChanges: raw.ip_changes,
    parallelSessions: raw.parallel_sessions,
    lastActivity: raw.last_activity,
    flags: raw.flags,
  }));

export type RosterRow = z.infer<typeof rosterRowSchema>;

export const rosterSchema = z
  .object({
    generated_at: z.string(),
    truncated: z.boolean(),
    rows: z.array(rosterRowSchema),
  })
  .transform((raw) => ({ generatedAt: raw.generated_at, truncated: raw.truncated, rows: raw.rows }));

export type Roster = z.infer<typeof rosterSchema>;

/**
 * What one feed item carries, by kind. `none` is a kind with nothing to add
 * (the clock starting) and also a kind or payload this build cannot read: the
 * row still says who did something and when, which is better than a feed that
 * fails whole over one unfamiliar line.
 */
export type FeedDetail =
  | {
      type: "query";
      id: number;
      sql: string;
      sqlTruncated: boolean;
      /** `running` until the query ends; see `monitor-feed.ts` for the refresh. */
      status: string;
      error?: string;
      durationMs: number | null;
      rowCount: number | null;
      ip?: string;
    }
  | { type: "answer"; questionOrd: number; attemptNo: number; value: string; correct: boolean; points: number }
  | { type: "audit"; ip?: string; userAgent?: string; reason?: string }
  | { type: "page_left"; awayMs: number }
  /** `count` is how many identical pastes in a row the line stands for: 1 for a single one. */
  | { type: "paste"; target: string; chars: number; text: string; count: number }
  | { type: "ip_changed"; from: string; to: string }
  | { type: "parallel_session"; otherIp: string; userAgent: string }
  | { type: "tab"; title?: string; from?: string; to?: string }
  | { type: "none" };

const queryData = z
  .object({
    id: z.number(),
    sql: z.string(),
    sql_truncated: z.boolean().optional(),
    status: z.string(),
    error: z.string().optional(),
    duration_ms: z.number().nullable().optional(),
    row_count: z.number().nullable().optional(),
    ip: z.string().optional(),
  })
  .transform(
    (raw): FeedDetail => ({
      type: "query",
      id: raw.id,
      sql: raw.sql,
      sqlTruncated: raw.sql_truncated ?? false,
      status: raw.status,
      error: raw.error,
      durationMs: raw.duration_ms ?? null,
      rowCount: raw.row_count ?? null,
      ip: raw.ip,
    }),
  );

const answerData = z
  .object({
    question_ord: z.number(),
    attempt_no: z.number(),
    value: z.string(),
    correct: z.boolean(),
    points_awarded: z.number(),
  })
  .transform(
    (raw): FeedDetail => ({
      type: "answer",
      questionOrd: raw.question_ord,
      attemptNo: raw.attempt_no,
      value: raw.value,
      correct: raw.correct,
      points: raw.points_awarded,
    }),
  );

const auditData = z
  .object({ ip: z.string().optional(), user_agent: z.string().optional(), reason: z.string().optional() })
  .transform((raw): FeedDetail => ({ type: "audit", ip: raw.ip, userAgent: raw.user_agent, reason: raw.reason }));

const tabData = z
  .object({ title: z.string().optional(), from: z.string().optional(), to: z.string().optional() })
  .transform((raw): FeedDetail => ({ type: "tab", title: raw.title, from: raw.from, to: raw.to }));

/** The reader of each kind's data; a kind absent here carries nothing to show. */
const DATA_BY_KIND: Record<string, z.ZodType<FeedDetail, unknown>> = {
  query: queryData,
  answer: answerData,
  sign_in: auditData,
  sign_out: auditData,
  sign_in_failed: auditData,
  disqualified: auditData,
  page_left: z
    .object({ away_ms: z.number() })
    .transform((raw): FeedDetail => ({ type: "page_left", awayMs: raw.away_ms })),
  paste: z
    .object({ target: z.string(), chars: z.number(), text: z.string(), count: z.number().optional() })
    .transform(
      (raw): FeedDetail => ({
        type: "paste",
        target: raw.target,
        chars: raw.chars,
        text: raw.text,
        // The server folds identical pastes in a row into one and omits the count for a single paste.
        count: Math.max(1, raw.count ?? 1),
      }),
    ),
  ip_changed: z
    .object({ from: z.string(), to: z.string() })
    .transform((raw): FeedDetail => ({ type: "ip_changed", from: raw.from, to: raw.to })),
  parallel_session: z
    .object({ other_ip: z.string(), user_agent: z.string() })
    .transform((raw): FeedDetail => ({ type: "parallel_session", otherIp: raw.other_ip, userAgent: raw.user_agent })),
  tab_created: tabData,
  tab_renamed: tabData,
  tab_deleted: tabData,
};

export const feedItemSchema = z
  .object({
    cursor: z.string(),
    at: z.string(),
    kind: z.string(),
    registration_id: z.string(),
    login: z.string(),
    full_name: z.string(),
    data: z.unknown(),
  })
  .transform((raw) => {
    const reader = DATA_BY_KIND[raw.kind];
    const parsed = reader?.safeParse(raw.data);
    const detail: FeedDetail = parsed?.success ? parsed.data : { type: "none" };
    return {
      cursor: raw.cursor,
      at: raw.at,
      kind: raw.kind,
      registrationId: raw.registration_id,
      login: raw.login,
      fullName: raw.full_name,
      detail,
    };
  });

export type FeedItem = z.infer<typeof feedItemSchema>;

export const feedSchema = z.object({
  /** Oldest first, whichever way the page was read. */
  items: z.array(feedItemSchema),
  /** More items past the page in the direction it was read. */
  more: z.boolean(),
  /** The cursors to ask `after=` and `before=` with next; absent on an empty page. */
  newest: z.string().optional(),
  oldest: z.string().optional(),
});

export type FeedPage = z.infer<typeof feedSchema>;

/** What a feed read may ask. `from` is inclusive, `until` exclusive. */
export type FeedParams = {
  after?: string;
  before?: string;
  kinds?: readonly string[];
  participant?: string;
  from?: string;
  until?: string;
  limit?: number;
};

/** The most a feed page carries (monitor.MaxFeedPage). */
export const MAX_FEED_PAGE = 200;

function monitorBase(contestId: string): string {
  return `/contests/${encodeURIComponent(contestId)}/monitor`;
}

function withQuery(path: string, query: URLSearchParams): string {
  const text = query.toString();
  return text ? `${path}?${text}` : path;
}

/** A feed read's parameters, in a fixed order; `participant` only where the route takes one. */
function feedQuery(params: FeedParams, withParticipant: boolean): URLSearchParams {
  const query = new URLSearchParams();
  if (params.after) query.set("after", params.after);
  if (params.before) query.set("before", params.before);
  if (params.kinds && params.kinds.length > 0) query.set("kinds", params.kinds.join(","));
  if (withParticipant && params.participant) query.set("participant", params.participant);
  if (params.from) query.set("from", params.from);
  if (params.until) query.set("until", params.until);
  if (params.limit) query.set("limit", String(params.limit));
  return query;
}

/** The feed's path with the parameters that were given, in a fixed order. */
export function feedPath(contestId: string, params: FeedParams): string {
  return withQuery(`${monitorBase(contestId)}/feed`, feedQuery(params, true));
}

/** One participant's routes: `…/monitor/participants/{registrationId}`. */
export function participantBase(contestId: string, registrationId: string): string {
  return `${monitorBase(contestId)}/participants/${encodeURIComponent(registrationId)}`;
}

/**
 * One participant's timeline: the feed's parameters, less `participant` —
 * the route names the participant itself.
 */
export function timelinePath(contestId: string, registrationId: string, params: FeedParams): string {
  return withQuery(`${participantBase(contestId, registrationId)}/timeline`, feedQuery(params, false));
}

/** The contest-wide CSV, as a link the browser downloads (see ExportMenu). */
export function monitorCsvHref(contestId: string): string {
  return `${API_PREFIX}${monitorBase(contestId)}/export.csv`;
}

/**
 * The shapes both audiences of the SQL log speak, re-exported so a reader of
 * the monitoring routes finds them where the routes are. They are defined in
 * `./query-log`, which the participant's own profile reads from too.
 */
export {
  answersSchema,
  loggedQuerySchema,
  queriesSchema,
  MAX_QUERIES_PAGE,
  MAX_QUERY_SEARCH,
  QUERY_STATUSES,
  type Answers,
  type Attempt,
  type LoggedQuery,
  type QueriesPage,
  type QueriesParams,
  type ReadOptions,
} from "./query-log";

/** GET …/monitor/participants, from the browser. */
export async function fetchRoster(contestId: string, options: ReadOptions = {}): Promise<Roster> {
  const payload = await request(`${monitorBase(contestId)}/participants`, {
    credentials: "same-origin",
    signal: options.signal,
  });
  return rosterSchema.parse(payload);
}

/** GET …/monitor/feed, from the browser. */
export async function fetchFeed(contestId: string, params: FeedParams, options: ReadOptions = {}): Promise<FeedPage> {
  const payload = await request(feedPath(contestId, params), { credentials: "same-origin", signal: options.signal });
  return feedSchema.parse(payload);
}

/** GET …/monitor/participants/{id}/timeline, from the browser. */
export async function fetchTimeline(
  contestId: string,
  registrationId: string,
  params: FeedParams,
  options: ReadOptions = {},
): Promise<FeedPage> {
  const payload = await request(timelinePath(contestId, registrationId, params), {
    credentials: "same-origin",
    signal: options.signal,
  });
  return feedSchema.parse(payload);
}

/** One participant's CSV, as a link the browser downloads. */
export function participantCsvHref(contestId: string, registrationId: string): string {
  return `${API_PREFIX}${participantBase(contestId, registrationId)}/export.csv`;
}

/** Who one participant is, for the heading of their page. */
export const participantSchema = z
  .object({
    registration_id: z.string(),
    login: z.string(),
    full_name: z.string(),
    status: z.string(),
    started_at: z.string().nullable(),
    finished_at: z.string().nullable(),
  })
  .transform((raw) => ({
    registrationId: raw.registration_id,
    login: raw.login,
    fullName: raw.full_name,
    status: raw.status,
    startedAt: raw.started_at,
    finishedAt: raw.finished_at,
  }));

export type Participant = z.infer<typeof participantSchema>;

export function queriesPath(contestId: string, registrationId: string, params: QueriesParams): string {
  const query = new URLSearchParams();
  if (params.status) query.set("status", params.status);
  if (params.q) query.set("q", params.q);
  if (params.cursor) query.set("cursor", params.cursor);
  if (params.limit) query.set("limit", String(params.limit));
  return withQuery(`${participantBase(contestId, registrationId)}/queries`, query);
}

/** GET …/monitor/participants/{id}/queries, from the browser. */
export async function fetchQueries(
  contestId: string,
  registrationId: string,
  params: QueriesParams,
  options: ReadOptions = {},
): Promise<QueriesPage> {
  const payload = await request(queriesPath(contestId, registrationId, params), {
    credentials: "same-origin",
    signal: options.signal,
  });
  return queriesSchema.parse(payload);
}

/** The document a notes revision belongs to; any other document is a tab's id. */
export const NOTES_DOCUMENT = "notes";

const revisionFields = {
  id: z.number(),
  document: z.string(),
  title: z.string(),
  started_at: z.string(),
  updated_at: z.string(),
  size: z.number(),
};

type WireRevision = { id: number; document: string; title: string; started_at: string; updated_at: string; size: number };

function readRevision(raw: WireRevision) {
  return {
    id: raw.id,
    document: raw.document,
    title: raw.title,
    startedAt: raw.started_at,
    updatedAt: raw.updated_at,
    /** The body's length in bytes. */
    size: raw.size,
  };
}

export type RevisionInfo = ReturnType<typeof readRevision>;

/** The notes and tabs now, and every revision without its body, newest first. */
export const workspaceSchema = z
  .object({
    notes: z.object({ body: z.string(), updated_at: z.string().nullable() }),
    tabs: z.array(
      z.object({ id: z.string(), title: z.string(), position: z.number(), body: z.string(), updated_at: z.string() }),
    ),
    revisions: z.array(z.object(revisionFields)),
    truncated: z.boolean(),
  })
  .transform((raw) => ({
    notes: { body: raw.notes.body, updatedAt: raw.notes.updated_at },
    tabs: raw.tabs.map((tab) => ({
      id: tab.id,
      title: tab.title,
      position: tab.position,
      body: tab.body,
      updatedAt: tab.updated_at,
    })),
    revisions: raw.revisions.map(readRevision),
    truncated: raw.truncated,
  }));

export type Workspace = z.infer<typeof workspaceSchema>;

export const revisionSchema = z
  .object({ ...revisionFields, body: z.string() })
  .transform((raw) => ({ ...readRevision(raw), body: raw.body }));

export type Revision = z.infer<typeof revisionSchema>;

/** GET …/monitor/participants/{id}/workspace/revisions/{revisionId}, from the browser. */
export async function fetchRevision(
  contestId: string,
  registrationId: string,
  revisionId: number,
  options: ReadOptions = {},
): Promise<Revision> {
  const payload = await request(
    `${participantBase(contestId, registrationId)}/workspace/revisions/${encodeURIComponent(String(revisionId))}`,
    { credentials: "same-origin", signal: options.signal },
  );
  return revisionSchema.parse(payload);
}

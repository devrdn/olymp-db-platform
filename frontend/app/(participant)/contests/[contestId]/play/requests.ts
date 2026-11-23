import type { StandingsResult } from "@/components/product/use-standings";
import { browserCall, browserRequest, type Refused } from "@/lib/api/browser";
import { queryResultSchema, type QueryResult } from "@/lib/api/console";
import { isId } from "@/lib/api/ids";
import { standingsSchema } from "@/lib/api/leaderboard";
import {
  askedSchema,
  clarificationFeedSchema,
  type ClarificationFeed,
  type SeenClarification,
} from "@/lib/api/clarifications";
import { answerResultSchema, playQuestionListSchema, type AnswerResult, type PlayQuestion } from "@/lib/api/play";
import { queryLogEntrySchema, queryLogResponseSchema, type QueryLogEntry } from "@/lib/api/querylog";
import { gameSchemaSchema, type GameSchema } from "@/lib/api/schema";
import type { Locale } from "@/lib/i18n/config";

/**
 * The play screen's calls to the API, made from the browser with the session
 * cookie, like the autosave in `lib/api/workspace.ts`.
 *
 * Not server actions: Next dispatches a page's server actions one at a time,
 * so an answer, the standings poll and the log would wait behind a running
 * query (up to twenty seconds), and in the last minute of a contest an answer
 * sent on time would arrive as `deadline_passed`. A browser request also
 * keeps a 5 MiB result out of the Next process, which otherwise held it as
 * text, parsed, validated and re-encoded.
 *
 * Each function returns a state object rather than throwing, so a form can
 * bind it with `useActionState` and show the code with `messageForCode`.
 */

/**
 * What the console shows after a run: an answer, or why there is none. A
 * union rather than a result with an optional error, so stale rows are never
 * shown beside a refusal.
 */
export type ConsoleState = { kind: "idle" } | { kind: "answer"; result: QueryResult } | Refused;

/** Runs one query as the signed-in participant. */
export async function runQuery(_previous: ConsoleState, form: FormData): Promise<ConsoleState> {
  const contestId = String(form.get("contestId") ?? "");
  // Not trimmed: a syntax error's position is an offset into exactly this
  // string, which is also what the editor holds.
  const sql = String(form.get("sql") ?? "");

  // An empty console is not a query; sending it would spend the rate limit
  // on a parse error.
  if (sql.trim() === "") return { kind: "idle" };
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };

  return browserCall(async () => ({
    kind: "answer" as const,
    result: queryResultSchema.parse(await browserRequest(`/contests/${contestId}/query`, { method: "POST", body: { sql } })),
  }));
}

/** What a question's form shows after a submission; shaped like ConsoleState. */
export type AnswerState = { kind: "idle" } | { kind: "answer"; result: AnswerResult } | Refused;

/**
 * Submits one answer as the signed-in participant. Each question owns its
 * own state, so a refusal on one does not touch another.
 */
export async function submitAnswer(_previous: AnswerState, form: FormData): Promise<AnswerState> {
  const contestId = String(form.get("contestId") ?? "");
  const questionId = String(form.get("questionId") ?? "");
  const value = String(form.get("value") ?? "").trim();

  // An empty answer is not an attempt; sending it would spend one.
  if (value === "") return { kind: "idle" };
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (!isId(questionId)) return { kind: "refused", code: "invalid_question_id" };

  return browserCall(async () => ({
    kind: "answer" as const,
    result: answerResultSchema.parse(
      await browserRequest(`/contests/${contestId}/questions/${questionId}/answer`, { method: "POST", body: { value } }),
    ),
  }));
}

/** The re-read question list, or why it could not be read. */
export type QuestionsRefreshResult = { kind: "ok"; items: PlayQuestion[] } | Refused;

/**
 * Re-reads the question list. A closed question can open the next one in a
 * sequential contest, and the events channel has no push for that; a full
 * `router.refresh()` would also remount the console. Asks in the page's
 * language, so the list does not change language.
 */
export async function refreshQuestions(contestId: string, locale: Locale): Promise<QuestionsRefreshResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  return browserCall(async () => ({
    kind: "ok" as const,
    items: playQuestionListSchema.parse(await browserRequest(`/contests/${contestId}/play/questions?lang=${locale}`)).items,
  }));
}

/** A re-read page of the query log, or why it could not be read. */
export type QueryLogRefreshResult = { kind: "ok"; items: QueryLogEntry[]; total: number } | Refused;

/**
 * Re-reads one page of the participant's query log. The server scopes it to
 * the session's registration, never to a value in the request. Called by
 * QueryLogPanel when it is shown, on "load more" and on retry. `before`, a
 * row id, asks for the rows older than that one.
 */
export async function fetchQueryLog(
  contestId: string,
  limit: number,
  offset: number,
  before?: number,
): Promise<QueryLogRefreshResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  const cursor = before !== undefined && Number.isSafeInteger(before) && before > 0 ? `&before=${before}` : "";
  return browserCall(async () => ({
    kind: "ok" as const,
    ...queryLogResponseSchema.parse(
      await browserRequest(`/contests/${contestId}/play/log?limit=${limit}&offset=${offset}${cursor}`),
    ),
  }));
}

export type QueryLogEntryResult = { kind: "ok"; entry: QueryLogEntry } | Refused;

/**
 * Reads one row of the participant's own log with its statement whole, which
 * a page cuts. The server answers any row that is not the caller's as
 * `not_found`, and so does this for an id no row can have.
 */
export async function fetchQueryLogEntry(contestId: string, id: number): Promise<QueryLogEntryResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (!Number.isSafeInteger(id) || id <= 0) return { kind: "refused", code: "not_found" };
  return browserCall(async () => ({
    kind: "ok" as const,
    entry: queryLogEntrySchema.parse(await browserRequest(`/contests/${contestId}/play/log/${id}`)),
  }));
}

/**
 * Reads the contest's standings as this participant sees them: the public
 * table plus which row is theirs, titled in the page's language.
 */
export async function fetchStandings(contestId: string, locale: Locale): Promise<StandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  return browserCall(async () => ({
    kind: "ok" as const,
    standings: standingsSchema.parse(await browserRequest(`/contests/${contestId}/play/leaderboard?lang=${locale}`)),
  }));
}

/** What the reset control shows after a press. */
export type ResetState = { kind: "idle" } | { kind: "done" } | Refused;

/**
 * Gives the participant their database back as the contest started it. The
 * API decides whether the contest allows it and how often; a press it
 * refuses still counts against that limit.
 */
export async function resetDatabase(_previous: ResetState, form: FormData): Promise<ResetState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };

  return browserCall(async () => {
    await browserRequest(`/contests/${contestId}/play/reset`, { method: "POST" });
    return { kind: "done" as const };
  });
}

/** The re-read schema, or why it could not be read. */
export type SchemaRefreshResult = { kind: "ok"; schema: GameSchema } | Refused;

/** Re-reads the schema of the contest's current game build. */
export async function refreshSchema(contestId: string): Promise<SchemaRefreshResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  return browserCall(async () => ({
    kind: "ok" as const,
    schema: gameSchemaSchema.parse(await browserRequest(`/contests/${contestId}/play/schema`)),
  }));
}

/** A page of the questions to the organisers, or why it could not be read. */
export type ClarificationsResult = { kind: "ok"; feed: ClarificationFeed } | Refused;

/**
 * Reads one page of the participant's questions to the organisers, the public
 * answers and the announcements; `before` is the cursor the previous page
 * gave. The server scopes it to the session's registration.
 */
export async function fetchClarifications(contestId: string, before?: string): Promise<ClarificationsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  const cursor = before ? `?before=${encodeURIComponent(before)}` : "";
  return browserCall(async () => ({
    kind: "ok" as const,
    feed: clarificationFeedSchema.parse(await browserRequest(`/contests/${contestId}/play/clarifications${cursor}`)),
  }));
}

export type AskResult = { kind: "ok"; asked: SeenClarification } | Refused;

/**
 * Sends a question to the organisers, about one of the contest's questions or
 * none. An empty question is not sent: it would spend one of the few asks a
 * minute allows.
 */
export async function askOrganisers(contestId: string, body: string, questionId: string | null): Promise<AskResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (body.trim() === "") return { kind: "refused", code: "clarification_empty" };
  return browserCall(async () => ({
    kind: "ok" as const,
    asked: askedSchema.parse(
      await browserRequest(`/contests/${contestId}/play/clarifications`, {
        method: "POST",
        body: questionId ? { body, question_id: questionId } : { body },
      }),
    ),
  }));
}

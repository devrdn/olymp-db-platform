"use server";

import type { StandingsResult } from "@/components/product/use-standings";
import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { standingsSchema } from "@/lib/api/leaderboard";
import { queryResultSchema, type QueryResult } from "@/lib/api/console";
import { answerResultSchema, playQuestionListSchema, type AnswerResult, type PlayQuestion } from "@/lib/api/play";
import { queryLogResponseSchema, type QueryLogEntry } from "@/lib/api/querylog";
import { gameSchemaSchema, type GameSchema } from "@/lib/api/schema";
import { serverRequest } from "@/lib/api/server";
import { activeLocale } from "@/lib/i18n/server";

/**
 * What the console shows after a run: an answer, or why there is none. A
 * union rather than a result with an optional error, so stale rows are never
 * shown beside a refusal.
 */
export type ConsoleState =
  | { kind: "idle" }
  | { kind: "answer"; result: QueryResult }
  | { kind: "refused"; code: string; subject?: string; requestId?: string; position?: number };

/**
 * Runs one query as the signed-in participant. A server action, so no token
 * reaches the browser.
 */
export async function runQueryAction(
  _previous: ConsoleState,
  form: FormData,
): Promise<ConsoleState> {
  const contestId = String(form.get("contestId") ?? "");
  // Not trimmed: a syntax error's position is an offset into exactly this
  // string, which is also what the editor holds.
  const sql = String(form.get("sql") ?? "");

  // An empty console is not a query; sending it would spend the rate limit
  // on a parse error.
  if (sql.trim() === "") return { kind: "idle" };
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };

  try {
    const payload = await serverRequest(`/contests/${contestId}/query`, {
      method: "POST",
      body: { sql },
    });
    return { kind: "answer", result: queryResultSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) {
      // The request id, so the participant can quote it.
      return {
        kind: "refused",
        code: error.code,
        subject: error.subject,
        requestId: error.requestId,
        position: error.position,
      };
    }
    return { kind: "refused", code: "unreachable" };
  }
}

/** What a question's form shows after a submission; shaped like ConsoleState. */
export type AnswerState =
  | { kind: "idle" }
  | { kind: "answer"; result: AnswerResult }
  | { kind: "refused"; code: string; subject?: string; requestId?: string };

/**
 * Submits one answer as the signed-in participant. Each question owns its
 * own action state, so a refusal on one does not touch another.
 */
export async function submitAnswerAction(
  _previous: AnswerState,
  form: FormData,
): Promise<AnswerState> {
  const contestId = String(form.get("contestId") ?? "");
  const questionId = String(form.get("questionId") ?? "");
  const value = String(form.get("value") ?? "").trim();

  // An empty answer is not an attempt; sending it would spend one.
  if (value === "") return { kind: "idle" };
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (!isId(questionId)) return { kind: "refused", code: "invalid_question_id" };

  try {
    const payload = await serverRequest(`/contests/${contestId}/questions/${questionId}/answer`, {
      method: "POST",
      body: { value },
    });
    return { kind: "answer", result: answerResultSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) {
      return {
        kind: "refused",
        code: error.code,
        subject: error.subject,
        requestId: error.requestId,
      };
    }
    return { kind: "refused", code: "unreachable" };
  }
}

/** The re-read question list, or why it could not be read. */
export type QuestionsRefreshResult =
  | { kind: "ok"; items: PlayQuestion[] }
  | { kind: "refused"; code: string };

/**
 * Re-reads the question list. A closed question can open the next one in a
 * sequential contest, and the events channel has no push for that; a full
 * `router.refresh()` would also remount the console. Asks in the active
 * language, as the page did, so the list does not change language.
 */
export async function refreshQuestionsAction(contestId: string): Promise<QuestionsRefreshResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  try {
    const locale = await activeLocale();
    const payload = await serverRequest(`/contests/${contestId}/play/questions?lang=${locale}`);
    return { kind: "ok", items: playQuestionListSchema.parse(payload).items };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

/** A re-read page of the query log, or why it could not be read. */
export type QueryLogRefreshResult =
  | { kind: "ok"; items: QueryLogEntry[]; total: number }
  | { kind: "refused"; code: string };

/**
 * Re-reads one page of the participant's query log. The server scopes it to
 * the session's registration, never to a value in the request. Called by
 * QueryLogPanel when it is shown, on "load more" and on retry.
 */
export async function fetchQueryLogAction(
  contestId: string,
  limit: number,
  offset: number,
): Promise<QueryLogRefreshResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  try {
    const payload = await serverRequest(`/contests/${contestId}/play/log?limit=${limit}&offset=${offset}`);
    const parsed = queryLogResponseSchema.parse(payload);
    return { kind: "ok", items: parsed.items, total: parsed.total };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

/**
 * Reads the contest's standings as this participant sees them: the public
 * table plus which row is theirs, titled in the participant's language.
 */
export async function fetchStandingsAction(contestId: string): Promise<StandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  try {
    const locale = await activeLocale();
    const payload = await serverRequest(`/contests/${contestId}/play/leaderboard?lang=${locale}`);
    return { kind: "ok", standings: standingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

/** What the reset control shows after a press. */
export type ResetState =
  | { kind: "idle" }
  | { kind: "done" }
  | { kind: "refused"; code: string; requestId?: string };

/**
 * Gives the participant their database back as the contest started it. The
 * API decides whether the contest allows it and how often; a press it
 * refuses still counts against that limit.
 */
export async function resetDatabaseAction(_previous: ResetState, form: FormData): Promise<ResetState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };

  try {
    await serverRequest(`/contests/${contestId}/play/reset`, { method: "POST" });
    return { kind: "done" };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code, requestId: error.requestId };
    return { kind: "refused", code: "unreachable" };
  }
}

/** The re-read schema, or why it could not be read. */
export type SchemaRefreshResult = { kind: "ok"; schema: GameSchema } | { kind: "refused"; code: string };

/**
 * Re-reads the schema of the participant's own database, which a reset
 * changes: the tables and views they made are gone.
 */
export async function refreshSchemaAction(contestId: string): Promise<SchemaRefreshResult> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };

  try {
    const payload = await serverRequest(`/contests/${contestId}/play/schema`);
    return { kind: "ok", schema: gameSchemaSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

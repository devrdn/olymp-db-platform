"use server";

import type { StandingsResult } from "@/components/product/use-standings";
import { ApiError } from "@/lib/api/client";
import { standingsSchema } from "@/lib/api/leaderboard";
import { queryResultSchema, type QueryResult } from "@/lib/api/console";
import { answerResultSchema, playQuestionListSchema, type AnswerResult, type PlayQuestion } from "@/lib/api/play";
import { queryLogResponseSchema, type QueryLogEntry } from "@/lib/api/querylog";
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
  if (!contestId || sql.trim() === "") return { kind: "idle" };

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
  if (!contestId || !questionId || value === "") return { kind: "idle" };

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
  try {
    const locale = await activeLocale();
    const payload = await serverRequest(`/contests/${contestId}/play/leaderboard?lang=${locale}`);
    return { kind: "ok", standings: standingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

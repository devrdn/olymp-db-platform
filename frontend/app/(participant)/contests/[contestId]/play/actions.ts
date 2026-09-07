"use server";

import { ApiError } from "@/lib/api/client";
import { queryResultSchema, type QueryResult } from "@/lib/api/console";
import { answerResultSchema, playQuestionListSchema, type AnswerResult, type PlayQuestion } from "@/lib/api/play";
import { serverRequest } from "@/lib/api/server";
import { activeLocale } from "@/lib/i18n/server";

/**
 * What the console shows after a run: an answer, or why there is none.
 *
 * A discriminated shape rather than a result with an optional error, because
 * the two are different screens — a table with a red box above it invites
 * reading stale rows as if they answered the question just asked.
 */
export type ConsoleState =
  | { kind: "idle" }
  | { kind: "answer"; result: QueryResult }
  | { kind: "refused"; code: string; subject?: string; requestId?: string };

/**
 * Runs one query as the signed-in participant.
 *
 * A server action, so the session cookie stays where it already is and no
 * token reaches the browser — the same reason every other write in this
 * interface is one.
 */
export async function runQueryAction(
  _previous: ConsoleState,
  form: FormData,
): Promise<ConsoleState> {
  const contestId = String(form.get("contestId") ?? "");
  const sql = String(form.get("sql") ?? "").trim();

  // An empty console is not a query. Sending it would spend the participant's
  // rate limit on nothing and answer with a parse error about the emptiness.
  if (!contestId || sql === "") return { kind: "idle" };

  try {
    const payload = await serverRequest(`/contests/${contestId}/query`, {
      method: "POST",
      body: { sql },
    });
    return { kind: "answer", result: queryResultSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) {
      // The identifier the API already puts in every error, carried so that
      // the participant can quote it. "It failed around two o'clock" is not
      // something anybody can grep for.
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

/**
 * What a question's own form shows after a submission: the outcome, or why
 * there is none — the same discriminated shape as the console's own state,
 * for the same reason (ConsoleState's own doc).
 */
export type AnswerState =
  | { kind: "idle" }
  | { kind: "answer"; result: AnswerResult }
  | { kind: "refused"; code: string; subject?: string; requestId?: string };

/**
 * Submits one answer to one question, as the signed-in participant.
 *
 * A server action, for the same reason runQueryAction is one: the session
 * cookie stays where it already sits, and no token reaches the browser.
 * Every question on the screen owns one of these independently — a refusal on
 * question 3 must not touch what question 2 is showing.
 */
export async function submitAnswerAction(
  _previous: AnswerState,
  form: FormData,
): Promise<AnswerState> {
  const contestId = String(form.get("contestId") ?? "");
  const questionId = String(form.get("questionId") ?? "");
  const value = String(form.get("value") ?? "").trim();

  // An empty answer is not an attempt. Sending it would spend one of the
  // participant's own attempts on nothing.
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

/** What re-reading the question list turned up, or why it could not be read. */
export type QuestionsRefreshResult =
  | { kind: "ok"; items: PlayQuestion[] }
  | { kind: "refused"; code: string };

/**
 * Re-reads the visible question list for one contest.
 *
 * Called directly from the client rather than through `useActionState` — it
 * has no form, only a moment that calls for fresh data: a question closing is
 * the one event that can change another question's `can_answer` (a
 * sequential contest opens the next one the instant this one is done with),
 * and the events channel carries no such per-question push. Scoped to just
 * the questions panel rather than a full `router.refresh()`, which would also
 * re-fetch the story and remount the console — exactly the re-render an
 * answer must not cause.
 *
 * Asks in the participant's own active language — the same cookie the page
 * itself read for the first copy of this list — rather than leaving it to
 * whatever the API falls back to without a `lang`: a refetch that quietly
 * answered in a different language than the page loaded in would be a small,
 * strange bug the moment a sequential question opens.
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

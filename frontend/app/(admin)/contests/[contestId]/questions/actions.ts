"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type QuestionListState = { code?: string };

/** Both identifiers reach a request path, so both are checked before they do. */
function ids(form: FormData): { contestId: string; questionId?: string } | null {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return null;

  const questionId = form.get("questionId");
  if (questionId !== null && !isId(questionId)) return null;

  return { contestId, questionId: isId(questionId) ? questionId : undefined };
}

async function attempt(
  path: string,
  init: { method: string; body?: unknown },
  contestId: string,
): Promise<QuestionListState> {
  const failure = await serverRequest(path, init).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // The list, the publish gate on the overview and the question's own screen
  // are all describing what just changed.
  revalidatePath(`/contests/${contestId}`, "layout");
  return {};
}

/**
 * A new question, created bare.
 *
 * A typed-answer question with no text and no options is valid on the Go side,
 * and that is the point: the author gets a row to open and fill in rather than
 * a modal demanding six decisions before anything exists. The publish gate is
 * what insists on the text and the reference answer, at the moment those
 * actually matter.
 */
export async function addQuestionAction(
  _previous: QuestionListState,
  form: FormData,
): Promise<QuestionListState> {
  const parsed = ids(form);
  if (!parsed) return { code: "invalid_contest_id" };

  return attempt(
    `/contests/${parsed.contestId}/questions`,
    {
      method: "POST",
      body: { kind: "text", points: 10, max_attempts: null, is_visible: true, choice_ids: [], texts: {} },
    },
    parsed.contestId,
  );
}

export async function deleteQuestionAction(
  _previous: QuestionListState,
  form: FormData,
): Promise<QuestionListState> {
  const parsed = ids(form);
  if (!parsed?.questionId) return { code: "invalid_question_id" };

  return attempt(
    `/contests/${parsed.contestId}/questions/${parsed.questionId}`,
    { method: "DELETE" },
    parsed.contestId,
  );
}

/**
 * Reordering, submitted as the whole new order rather than as a move.
 *
 * That is the endpoint's shape, and it is the right one: the repository does
 * the swap inside a transaction with a deferred uniqueness constraint, because
 * half way through exchanging two questions both rows hold the same position.
 * A "move question 3 up" API would have to reconstruct that order server-side
 * from a list the client already knows.
 */
export async function reorderQuestionsAction(
  _previous: QuestionListState,
  form: FormData,
): Promise<QuestionListState> {
  const parsed = ids(form);
  if (!parsed) return { code: "invalid_contest_id" };

  const order = form.getAll("order").map(String);
  if (order.length === 0 || !order.every(isId)) return { code: "invalid_question_id" };

  return attempt(
    `/contests/${parsed.contestId}/questions/order`,
    { method: "PUT", body: { order } },
    parsed.contestId,
  );
}

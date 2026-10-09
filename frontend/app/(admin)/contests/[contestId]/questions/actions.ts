"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type QuestionListState = { code?: string };

/** Both ids reach a request path, so both are validated first. */
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

  // The list, the publish gate and the question screen all changed.
  revalidatePath(`/contests/${contestId}`, "layout");
  return {};
}

/**
 * Creates a bare question, valid on the server, so the author gets a row to
 * fill in; the publish gate demands text and answers later.
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
 * Submits the whole new order: the repository swaps inside a transaction with a
 * deferred uniqueness constraint, since mid-swap both rows share a position.
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

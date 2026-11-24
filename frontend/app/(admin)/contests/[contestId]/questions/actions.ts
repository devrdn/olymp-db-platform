"use server";

import { isId } from "@/lib/api/ids";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";

import { contestChanged } from "../revalidation";

export type QuestionListState = ActionState;

/** Both ids reach a request path, so both are validated first. */
function ids(form: FormData): { contestId: string; questionId?: string } | null {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return null;

  const questionId = form.get("questionId");
  if (questionId !== null && !isId(questionId)) return null;

  return { contestId, questionId: isId(questionId) ? questionId : undefined };
}

/** The list, the publish gate and the question screen all change. */
function change(contestId: string, path: string, init: { method: string; body?: unknown }): Promise<QuestionListState> {
  return serverAction(() => serverRequest(path, init), { revalidate: contestChanged(contestId) });
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

  return change(
    parsed.contestId,
    `/contests/${parsed.contestId}/questions`,
    {
      method: "POST",
      body: { kind: "text", points: 10, max_attempts: null, is_visible: true, choice_ids: [], texts: {} },
    },
  );
}

export async function deleteQuestionAction(
  _previous: QuestionListState,
  form: FormData,
): Promise<QuestionListState> {
  const parsed = ids(form);
  if (!parsed?.questionId) return { code: "invalid_question_id" };

  return change(
    parsed.contestId,
    `/contests/${parsed.contestId}/questions/${parsed.questionId}`,
    { method: "DELETE" },
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

  return change(
    parsed.contestId,
    `/contests/${parsed.contestId}/questions/order`,
    { method: "PUT", body: { order } },
  );
}

"use server";

import { isId } from "@/lib/api/ids";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";

import { questionFrom } from "./question-form";

export type QuestionState = ActionState;

type Ids = { contestId: string; questionId: string };

function ids(form: FormData): Ids | null {
  const contestId = form.get("contestId");
  const questionId = form.get("questionId");
  return isId(contestId) && isId(questionId) ? { contestId, questionId } : null;
}

/**
 * Saves the whole question in one request, written in one transaction, so no
 * half-saved state is possible and a change of kind moves with its options and
 * answers. Parsing lives in `question-form.ts`.
 */
export async function saveQuestionAction(
  _previous: QuestionState,
  form: FormData,
): Promise<QuestionState> {
  const at = ids(form);
  if (!at) return { code: "invalid_question_id" };

  const parsed = questionFrom(form);
  if (!parsed.ok) return { code: parsed.code };

  return serverAction(
    () => serverRequest(`/contests/${at.contestId}/questions/${at.questionId}`, { method: "PUT", body: parsed.body }),
    { revalidate: [{ layout: `/contests/${at.contestId}` }] },
  );
}

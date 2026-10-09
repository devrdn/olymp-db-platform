"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

import { questionFrom } from "./question-form";

export type QuestionState = { code?: string; saved?: boolean };

type Ids = { contestId: string; questionId: string };

function ids(form: FormData): Ids | null {
  const contestId = form.get("contestId");
  const questionId = form.get("questionId");
  return isId(contestId) && isId(questionId) ? { contestId, questionId } : null;
}

async function attempt(path: string, init: { method: string; body: unknown }, at: Ids) {
  const failure = await serverRequest(path, init).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    return { code: failureCode(failure) };
  }

  revalidatePath(`/contests/${at.contestId}`, "layout");
  return { saved: true };
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

  return attempt(
    `/contests/${at.contestId}/questions/${at.questionId}`,
    { method: "PUT", body: parsed.body },
    at,
  );
}

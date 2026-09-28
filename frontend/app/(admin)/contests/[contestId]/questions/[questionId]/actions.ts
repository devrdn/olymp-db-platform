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
 * Saving the question.
 *
 * One action, one request, one button. It was three of each — the question's
 * own fields, its wording, its reference answers — and that was not only three
 * presses: the second was free to fail after the first had committed, leaving
 * a half-saved question under a button that had already said "saved".
 *
 * It also made one change impossible rather than merely tedious. Turning a
 * typed question into a choice question needs the kind, the options and the
 * answers to move together; sent separately, each request saw half the change
 * and refused on account of the other half. The API now takes the whole
 * question and writes it in one transaction, so both halves are known at once.
 *
 * The parsing lives in `question-form.ts`, where it has tests.
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

"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { MATCH_KINDS, QUESTION_KINDS, type MatchKind } from "@/lib/api/content";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

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
    return { code: failure instanceof ApiError ? failure.code : "unreachable" };
  }

  revalidatePath(`/contests/${at.contestId}`, "layout");
  return { saved: true };
}

/**
 * The question's own shape: what kind it is, what it is worth, how many
 * attempts it allows, whether it is shown, and — for a choice question — the
 * identifiers of its options.
 *
 * The option identifiers are language-independent by design and are edited
 * here rather than with the labels: a participant's answer is an identifier,
 * so renaming a label in Romanian must not change what a correct answer is.
 */
export async function saveQuestionAction(
  _previous: QuestionState,
  form: FormData,
): Promise<QuestionState> {
  const at = ids(form);
  if (!at) return { code: "invalid_question_id" };

  const kind = String(form.get("kind") ?? "");
  if (!(QUESTION_KINDS as readonly string[]).includes(kind)) return { code: "invalid_request" };

  const points = Number(form.get("points"));
  if (!Number.isFinite(points) || points < 0) return { code: "invalid_request" };

  // Unlimited is the absent field, never zero: a question allowing zero
  // attempts is one nobody can answer, which is never what was meant.
  const attempts = Number(form.get("maxAttempts"));
  const maxAttempts = Number.isFinite(attempts) && attempts > 0 ? Math.floor(attempts) : null;

  // Only a choice question has options, and the API refuses them on any other
  // kind — so switching a question back to typed text has to drop them here.
  const choiceIds =
    kind === "choice"
      ? [
          ...new Set(
            String(form.get("choiceIds") ?? "")
              .split(/[\s,;]+/)
              .map((id) => id.trim())
              .filter(Boolean),
          ),
        ]
      : [];

  return attempt(
    `/contests/${at.contestId}/questions/${at.questionId}`,
    {
      method: "PATCH",
      body: {
        kind,
        points: Math.floor(points),
        max_attempts: maxAttempts,
        is_visible: form.get("isVisible") === "on",
        choice_ids: choiceIds,
      },
    },
    at,
  );
}

/**
 * The question's text in every declared language, plus the labels for its
 * options, replaced as one set — which is the shape the endpoint takes and the
 * shape the publish gate reasons about.
 *
 * A language whose body is blank is dropped rather than sent empty. "Not
 * written yet" and "deliberately empty" are different facts, and an empty
 * string would satisfy the gate's presence check and publish a question that
 * asks its Romanian readers nothing.
 */
export async function saveTextsAction(
  _previous: QuestionState,
  form: FormData,
): Promise<QuestionState> {
  const at = ids(form);
  if (!at) return { code: "invalid_question_id" };

  const texts: Record<string, { body_md: string; choices?: Record<string, string> }> = {};

  for (const [key, value] of form.entries()) {
    if (!key.startsWith("body.")) continue;
    const body = String(value).trim();
    if (body) texts[key.slice("body.".length)] = { body_md: body };
  }

  // Labels only follow a body. Attached to a language with no text they would
  // be options for a question that does not exist in that language.
  for (const [key, value] of form.entries()) {
    if (!key.startsWith("choice.")) continue;
    const [, lang, choiceId] = key.split(".");
    const label = String(value).trim();
    if (!label || !texts[lang]) continue;
    (texts[lang].choices ??= {})[choiceId] = label;
  }

  return attempt(
    `/contests/${at.contestId}/questions/${at.questionId}/texts`,
    { method: "PUT", body: { texts } },
    at,
  );
}

/**
 * The reference answers, replaced as a set.
 *
 * They carry no language: the game database is English throughout, so an
 * answer read out of it is English whatever language the story was told in. A
 * transliterated spelling is another row, not another translation.
 *
 * A regular expression that does not compile is rejected at authoring time by
 * the API. Discovered instead in the middle of a running contest, it breaks
 * marking for everyone who reached the question, at the one moment nobody is
 * available to fix it.
 */
export async function saveAnswersAction(
  _previous: QuestionState,
  form: FormData,
): Promise<QuestionState> {
  const at = ids(form);
  if (!at) return { code: "invalid_question_id" };

  const values = form.getAll("answerValue").map(String);
  const kinds = form.getAll("answerKind").map(String);

  const answers: { match_kind: MatchKind; value: string }[] = [];
  for (const [index, raw] of values.entries()) {
    const value = raw.trim();
    if (!value) continue;

    const matchKind = kinds[index];
    if (!(MATCH_KINDS as readonly string[]).includes(matchKind)) return { code: "invalid_request" };

    answers.push({ match_kind: matchKind as MatchKind, value });
  }

  return attempt(
    `/contests/${at.contestId}/questions/${at.questionId}/answers`,
    { method: "PUT", body: { answers } },
    at,
  );
}

import { MATCH_KINDS, QUESTION_KINDS, type MatchKind } from "@/lib/api/content";

/**
 * One editor form, read into one request.
 *
 * The question used to be saved in three: its own fields, its wording, its
 * reference answers — three buttons, three endpoints, three chances for the
 * second to fail after the first had landed. This reads the whole form at
 * once, and the API writes it in one transaction.
 *
 * Kept apart from the Server Action so the parsing has tests. It is where all
 * the judgement is: what an empty box means, which fields survive a change of
 * kind, and which half-filled row is not an answer.
 */

export type QuestionBody = {
  kind: string;
  points: number;
  max_attempts: number | null;
  is_visible: boolean;
  choice_ids: string[];
  texts: Record<string, { body_md: string; choices?: Record<string, string> }>;
  answers: { match_kind: MatchKind; value: string }[];
};

export type Parsed = { ok: true; body: QuestionBody } | { ok: false; code: string };

export function questionFrom(form: FormData): Parsed {
  const kind = String(form.get("kind") ?? "");
  if (!(QUESTION_KINDS as readonly string[]).includes(kind)) return { ok: false, code: "invalid_request" };

  const points = Number(form.get("points"));
  if (!Number.isFinite(points) || points < 0) return { ok: false, code: "invalid_request" };

  // Unlimited is the absent field, never zero: a question allowing zero
  // attempts is one nobody can answer, which is never what an empty box meant.
  const attempts = Number(form.get("maxAttempts"));
  const max_attempts = Number.isFinite(attempts) && attempts > 0 ? Math.floor(attempts) : null;

  // Only a choice question has options, and the API refuses them on any other
  // kind — so switching back to typed text has to let go of them here. The
  // identifiers are language-independent by design: a participant's answer is
  // an identifier, so renaming a label in Romanian must not change what a
  // correct answer is.
  const choice_ids =
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

  const texts: QuestionBody["texts"] = {};
  for (const [key, value] of form.entries()) {
    if (!key.startsWith("body.")) continue;
    const body = String(value).trim();
    // A language whose body is blank is dropped rather than sent empty. "Not
    // written yet" and "deliberately empty" are different facts, and an empty
    // string satisfies the publish gate's presence check — publishing a
    // question that asks its Romanian readers nothing.
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

  const values = form.getAll("answerValue").map(String);
  const kinds = form.getAll("answerKind").map(String);

  const answers: QuestionBody["answers"] = [];
  for (const [index, raw] of values.entries()) {
    const value = raw.trim();
    // The editor offers a spare row; an untouched one is not an answer.
    if (!value) continue;

    const matchKind = kinds[index];
    if (!(MATCH_KINDS as readonly string[]).includes(matchKind)) {
      return { ok: false, code: "invalid_request" };
    }
    answers.push({ match_kind: matchKind as MatchKind, value });
  }

  return {
    ok: true,
    body: {
      kind,
      points: Math.floor(points),
      max_attempts,
      is_visible: form.get("isVisible") === "on",
      choice_ids,
      texts,
      answers,
    },
  };
}

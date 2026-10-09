import { MATCH_KINDS, QUESTION_KINDS, type MatchKind } from "@/lib/api/content";

/**
 * Reads the whole editor form into one request, which the API writes in one
 * transaction. Separate from the Server Action so the parsing, where the
 * judgement is, has tests.
 */

export type QuestionBody = {
  kind: string;
  points: number;
  max_attempts: number | null;
  // `null` leaves the stored penalty alone; zero is a real setting (no
  // penalty).
  penalty_pct: number | null;
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

  // Unlimited is the absent field, never zero: zero attempts could never be
  // answered.
  const attempts = Number(form.get("maxAttempts"));
  const max_attempts = Number.isFinite(attempts) && attempts > 0 ? Math.floor(attempts) : null;

  // Blank leaves the stored penalty alone rather than sending zero, which would
  // clear a value set through the API.
  const rawPenalty = String(form.get("penaltyPct") ?? "").trim();
  let penalty_pct: number | null = null;
  if (rawPenalty !== "") {
    const parsedPenalty = Number(rawPenalty);
    if (!Number.isFinite(parsedPenalty) || parsedPenalty < 0 || parsedPenalty > 100) {
      return { ok: false, code: "invalid_request" };
    }
    penalty_pct = Math.floor(parsedPenalty);
  }

  // The API refuses options on other kinds, so they are dropped here. Ids are
  // language-independent: an answer is an id, so relabelling must not change
  // what is correct.
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
    // A blank body is dropped: an empty string would pass the publish gate's
    // presence check.
    if (body) texts[key.slice("body.".length)] = { body_md: body };
  }

  // Labels only for languages that have a body.
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
    // An untouched spare row is not an answer.
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
      penalty_pct,
      is_visible: form.get("isVisible") === "on",
      choice_ids,
      texts,
      answers,
    },
  };
}

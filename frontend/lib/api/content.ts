import { z } from "zod";


import { MATCH_KINDS, QUESTION_KINDS } from "./content-terms";

export { MATCH_KINDS, QUESTION_KINDS, answerable, untranslated } from "./content-terms";


/**
 * The wire shapes of what an author writes: the crime story and the questions.
 *
 * Split from `contests.ts` because it is a separate set of endpoints with a
 * separate freeze date — content stops being editable at the start, while the
 * contest's own settings stay open — and because keeping them together would
 * make one module the place to look for everything, which is the same as
 * having no place to look.
 */

export type QuestionKind = (typeof QUESTION_KINDS)[number];
export type MatchKind = (typeof MATCH_KINDS)[number];

/**
 * The story, as a translation per language.
 *
 * There is no untranslated body. The authored text moved into the translation
 * tables entirely; a copy on the base row "for the default language" would be
 * a second source of truth for one fact.
 */
export const storySchema = z
  .object({
    id: z.string(),
    translations: z.record(z.string(), z.string()),
    updated_at: z.string().optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    translations: raw.translations,
    updatedAt: raw.updated_at,
  }));

export type Story = z.infer<typeof storySchema>;

/**
 * One question's text in one language.
 *
 * `choices` maps the question's own stable identifiers to labels. The
 * identifiers are language-independent on purpose: a participant's answer is
 * an identifier, never a label, so checking a choice question does not depend
 * on the language it was read in.
 */
export const questionTextSchema = z
  .object({
    body_md: z.string(),
    choices: z.record(z.string(), z.string()).optional(),
  })
  .transform((raw) => ({ bodyMd: raw.body_md, choices: raw.choices ?? {} }));

export type QuestionText = z.infer<typeof questionTextSchema>;

/**
 * A reference answer.
 *
 * It has no language, deliberately: the game database is English throughout,
 * so an answer read out of it is English whatever language the story was told
 * in. A transliterated spelling is simply another row.
 */
export const answerSchema = z
  .object({
    id: z.string().optional(),
    match_kind: z.enum(MATCH_KINDS),
    value: z.string(),
  })
  .transform((raw) => ({ id: raw.id, matchKind: raw.match_kind, value: raw.value }));

export type Answer = z.infer<typeof answerSchema>;

export const questionSchema = z
  .object({
    id: z.string(),
    ord: z.number(),
    kind: z.enum(QUESTION_KINDS),
    points: z.number(),
    max_attempts: z.number().nullish(),
    // §6.1.1: percent of this question's own points, lost per wrong attempt.
    // Always returned — zero is "no penalty", a meaningful value in its own
    // right, not an absent one, the same distinction the API's own
    // QuestionResponse.PenaltyPct doc makes.
    penalty_pct: z.number(),
    is_visible: z.boolean(),
    choice_ids: z.array(z.string()),
    texts: z.record(z.string(), questionTextSchema),
    answers: z.array(answerSchema).optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    ord: raw.ord,
    kind: raw.kind,
    points: raw.points,
    maxAttempts: raw.max_attempts ?? undefined,
    penaltyPct: raw.penalty_pct,
    /**
     * A hidden question exists in full — points, reference answers and all —
     * it is simply not shown. Working out what is being asked is part of the
     * task rather than a line of instructions.
     */
    isVisible: raw.is_visible,
    choiceIds: raw.choice_ids,
    texts: raw.texts,
    answers: raw.answers ?? [],
  }));

export type Question = z.infer<typeof questionSchema>;

export const questionListSchema = z.object({ items: z.array(questionSchema) });

/** Which languages still have no text for this question. */
/**
 * Whether a question can be answered at all.
 *
 * A question with no reference answer cannot be marked, and the author finds
 * out at the publish gate rather than while writing it. Naming it in the list
 * is cheaper for everyone.
 */

import { z } from "zod";


import { MATCH_KINDS, QUESTION_KINDS } from "./content-terms";

export { MATCH_KINDS, QUESTION_KINDS, answerable, untranslated } from "./content-terms";


/**
 * The wire shapes of what an author writes: the story and the questions.
 * Separate from `contests.ts`: other endpoints, and content freezes at the
 * start while settings stay open.
 */

export type QuestionKind = (typeof QUESTION_KINDS)[number];
export type MatchKind = (typeof MATCH_KINDS)[number];

/** The story, as a translation per language; there is no untranslated body. */
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
 * One question's text in one language. `choices` maps language-independent
 * choice ids to labels; an answer is an id, so checking ignores the language.
 */
export const questionTextSchema = z
  .object({
    body_md: z.string(),
    choices: z.record(z.string(), z.string()).optional(),
  })
  .transform((raw) => ({ bodyMd: raw.body_md, choices: raw.choices ?? {} }));

export type QuestionText = z.infer<typeof questionTextSchema>;

/**
 * A reference answer. It has no language: the game database is English, and a
 * transliterated spelling is another row.
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
    // Percent of the question's points lost per wrong attempt
    // (docs/ARCHITECTURE.md §6.1.1); zero means no penalty.
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
    /** A hidden question exists in full but is not shown; finding it is part of the task. */
    isVisible: raw.is_visible,
    choiceIds: raw.choice_ids,
    texts: raw.texts,
    answers: raw.answers ?? [],
  }));

export type Question = z.infer<typeof questionSchema>;

export const questionListSchema = z.object({ items: z.array(questionSchema) });

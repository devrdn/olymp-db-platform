import { z } from "zod";

import { QUESTION_KINDS } from "./content-terms";

/**
 * The participant's view of a running contest: the story in their language,
 * the questions they may see, and what answering one tells them. Kept apart
 * from content.ts (the staff view) so the narrower shapes of
 * docs/ARCHITECTURE.md §6.1 cannot be confused with it.
 */

/** The story, negotiated to one language. */
export const playStorySchema = z
  .object({ lang: z.string(), body_md: z.string() })
  .transform((raw) => ({ lang: raw.lang, bodyMd: raw.body_md }));

export type PlayStory = z.infer<typeof playStorySchema>;

/**
 * One question as its participant sees it, with no reference answer or
 * `max_attempts`. `attemptsRemaining` undefined means no cap, not zero left.
 */
export const playQuestionSchema = z
  .object({
    id: z.string(),
    kind: z.enum(QUESTION_KINDS),
    points: z.number(),
    choice_ids: z.array(z.string()),
    body_md: z.string(),
    choices: z.record(z.string(), z.string()).optional(),
    attempts_remaining: z.number().nullish(),
    closed: z.boolean(),
    /**
     * False while not `closed` means a sequential contest has not reached this
     * question yet. Decided by the server, never inferred from list position.
     */
    can_answer: z.boolean(),
    /**
     * Whether one of the participant's own attempts was right, and what it
     * earned, so a closed question shows whether it was won or ran out.
     */
    correct: z.boolean(),
    points_awarded: z.number(),
  })
  .transform((raw) => ({
    id: raw.id,
    kind: raw.kind,
    points: raw.points,
    choiceIds: raw.choice_ids,
    bodyMd: raw.body_md,
    choices: raw.choices ?? {},
    attemptsRemaining: raw.attempts_remaining ?? undefined,
    closed: raw.closed,
    canAnswer: raw.can_answer,
    correct: raw.correct,
    pointsAwarded: raw.points_awarded,
  }));

export type PlayQuestion = z.infer<typeof playQuestionSchema>;

export const playQuestionListSchema = z.object({
  lang: z.string(),
  items: z.array(playQuestionSchema),
});

export type PlayQuestionList = z.infer<typeof playQuestionListSchema>;

/** What answering tells the participant: never a reference answer. */
export const answerResultSchema = z
  .object({
    correct: z.boolean(),
    points_awarded: z.number(),
    attempts_remaining: z.number().nullish(),
    closed: z.boolean(),
  })
  .transform((raw) => ({
    correct: raw.correct,
    pointsAwarded: raw.points_awarded,
    attemptsRemaining: raw.attempts_remaining ?? undefined,
    closed: raw.closed,
  }));

export type AnswerResult = z.infer<typeof answerResultSchema>;

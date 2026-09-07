import { z } from "zod";

import { QUESTION_KINDS } from "./content-terms";

/**
 * The wire shapes of a participant's own working view of a running contest:
 * the story in their language, the questions they may currently see, and what
 * answering one tells them back.
 *
 * Apart from content.ts, which is the same nouns as staff see them — every
 * translation, every reference answer, every hidden question. This module
 * never carries either: the server already narrowed both to what §6.1 allows
 * a participant to read, and parsing a narrower shape here is what would
 * catch the two ever being confused, rather than a client quietly rendering a
 * field the participant endpoint was never meant to send.
 */

/** The story, negotiated to one language rather than every translation. */
export const playStorySchema = z
  .object({ lang: z.string(), body_md: z.string() })
  .transform((raw) => ({ lang: raw.lang, bodyMd: raw.body_md }));

export type PlayStory = z.infer<typeof playStorySchema>;

/**
 * One question as its participant sees it.
 *
 * No reference answer and no raw `max_attempts` field exist here, because
 * they cannot: the API's own response never carries them (participant_handler.go's
 * own doc). `attemptsRemaining` absent means no cap, not zero attempts left —
 * the API omits the field rather than sending zero for "unlimited", and this
 * schema keeps that distinction as `undefined` rather than collapsing it.
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
     * Whether this exact question may be answered right now. False without
     * `closed` also being true means a sequential contest has not reached it
     * yet — a fact from the server, never inferred from the question's own
     * position in the list.
     */
    can_answer: z.boolean(),
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
  }));

export type PlayQuestion = z.infer<typeof playQuestionSchema>;

export const playQuestionListSchema = z.object({
  lang: z.string(),
  items: z.array(playQuestionSchema),
});

export type PlayQuestionList = z.infer<typeof playQuestionListSchema>;

/**
 * What answering a question tells the participant back — never a reference
 * answer, only the same two derived facts a follow-up read of the question
 * list would already show (answerResponse's own doc on the Go side).
 */
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

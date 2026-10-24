import { z } from "zod";

/**
 * Questions to the organisers, their answers and announcements
 * (docs/ARCHITECTURE.md §6.5): the participant's feed and the staff queue.
 *
 * A participant's item never names who asked: `mine` is all the server sends.
 * Times carry exactly six fractional digits in UTC, so they compare as text in
 * the order they happened, which `latestStamp` relies on.
 */

/** The most characters a question may hold (`clarifications.MaxQuestionRunes`). */
export const QUESTION_MAX_CHARS = 2000;

/** The most characters an answer may hold (`clarifications.MaxAnswerRunes`). */
export const ANSWER_MAX_CHARS = 4000;

/** The most characters an announcement may hold (`clarifications.MaxAnnouncementRunes`). */
export const ANNOUNCEMENT_MAX_CHARS = 4000;

/** How many of one participant's questions may wait at once (`clarifications.MaxOpenPerRegistration`). */
export const MAX_OPEN_QUESTIONS = 20;

/** One page of the queue or the announcements; the server's `DefaultPageSize`. */
export const CLARIFICATION_PAGE_SIZE = 50;

const visibilitySchema = z.enum(["private", "public"]);

export type Visibility = z.infer<typeof visibilitySchema>;

const seenAnswerSchema = z
  .object({
    body: z.string(),
    declined: z.boolean(),
    visibility: visibilitySchema,
    answered_at: z.string(),
    edited_at: z.string().nullable(),
  })
  .transform((raw) => ({
    body: raw.body,
    declined: raw.declined,
    visibility: raw.visibility,
    answeredAt: raw.answered_at,
    editedAt: raw.edited_at,
  }));

const seenClarificationSchema = z
  .object({
    kind: z.literal("clarification"),
    id: z.string().min(1),
    posted_at: z.string(),
    mine: z.boolean(),
    question_id: z.string().nullable(),
    body: z.string(),
    asked_at: z.string(),
    answer: seenAnswerSchema.nullable(),
  })
  .transform((raw) => ({
    kind: "clarification" as const,
    id: raw.id,
    postedAt: raw.posted_at,
    mine: raw.mine,
    questionId: raw.question_id,
    body: raw.body,
    askedAt: raw.asked_at,
    answer: raw.answer,
  }));

export type SeenClarification = z.infer<typeof seenClarificationSchema>;

const seenAnnouncementSchema = z
  .object({
    kind: z.literal("announcement"),
    id: z.string().min(1),
    posted_at: z.string(),
    body: z.string(),
  })
  .transform((raw) => ({ kind: "announcement" as const, id: raw.id, postedAt: raw.posted_at, body: raw.body }));

export type SeenAnnouncement = z.infer<typeof seenAnnouncementSchema>;

export const feedItemSchema = z.union([seenClarificationSchema, seenAnnouncementSchema]);

export type FeedItem = z.infer<typeof feedItemSchema>;

/** One page of GET …/play/clarifications, newest first. */
export const clarificationFeedSchema = z.object({
  items: z.array(feedItemSchema),
  /** The cursor of the next page, null on the last. */
  next: z.string().nullable(),
  /** How many of the reader's questions wait for an answer. */
  open: z.number().int().nonnegative(),
});

export type ClarificationFeed = z.infer<typeof clarificationFeedSchema>;

/** What POST …/play/clarifications answers: the new question, as the feed shows it. */
export const askedSchema = seenClarificationSchema;

/**
 * The newest moment anything in items changed that the reader did not do
 * themselves: an announcement, an answer, an edit. A question of their own
 * still waiting is their own act and never counts as news. Empty when there
 * is nothing.
 */
export function latestStamp(items: FeedItem[]): string {
  let latest = "";
  for (const item of items) {
    if (item.kind === "clarification" && item.mine && item.answer === null) continue;
    const stamps = [item.postedAt];
    if (item.kind === "clarification" && item.answer?.editedAt) stamps.push(item.answer.editedAt);
    for (const stamp of stamps) if (stamp > latest) latest = stamp;
  }
  return latest;
}

const staffAnswerSchema = z
  .object({
    body: z.string(),
    declined: z.boolean(),
    visibility: visibilitySchema,
    answered_at: z.string(),
    answered_by: z.string(),
    edited_at: z.string().nullable(),
  })
  .transform((raw) => ({
    body: raw.body,
    declined: raw.declined,
    visibility: raw.visibility,
    answeredAt: raw.answered_at,
    /** The login of who answered first; empty once that account is gone. */
    answeredBy: raw.answered_by,
    editedAt: raw.edited_at,
  }));

/** A question as its contest's staff see it, with who asked. */
export const staffClarificationSchema = z
  .object({
    id: z.string().min(1),
    question_id: z.string().nullable(),
    body: z.string(),
    created_at: z.string(),
    asker: z.object({ login: z.string(), full_name: z.string() }),
    answer: staffAnswerSchema.nullable(),
  })
  .transform((raw) => ({
    id: raw.id,
    questionId: raw.question_id,
    body: raw.body,
    createdAt: raw.created_at,
    asker: { login: raw.asker.login, fullName: raw.asker.full_name },
    answer: raw.answer,
  }));

export type StaffClarification = z.infer<typeof staffClarificationSchema>;

/** One page of GET …/clarifications, with both states' counts. */
export const clarificationQueueSchema = z.object({
  items: z.array(staffClarificationSchema),
  counts: z.object({ open: z.number().int().nonnegative(), answered: z.number().int().nonnegative() }),
});

export type ClarificationQueue = z.infer<typeof clarificationQueueSchema>;

export type QueueState = "open" | "answered";

export const announcementSchema = z
  .object({ id: z.string().min(1), body: z.string(), created_at: z.string() })
  .transform((raw) => ({ id: raw.id, body: raw.body, createdAt: raw.created_at }));

export type Announcement = z.infer<typeof announcementSchema>;

export const announcementListSchema = z.object({
  items: z.array(announcementSchema),
  total: z.number().int().nonnegative(),
});

export type AnnouncementList = z.infer<typeof announcementListSchema>;

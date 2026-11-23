import { z } from "zod";

import { browserCall, browserRequest, type Refused } from "./browser";
import { CLARIFICATION_PAGE_SIZE, QUEUE_MAX_SHOWN } from "./clarifications-terms";
import { isId } from "./ids";

/**
 * Questions to the organisers, their answers and announcements
 * (docs/ARCHITECTURE.md §6.5): the participant's feed and the staff queue.
 *
 * A participant's item never names who asked: `mine` is all the server sends.
 * Times carry exactly six fractional digits in UTC, so they compare as text in
 * the order they happened, which `latestStamp` (`./clarifications-terms`)
 * relies on.
 */

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

/**
 * A staff read or write, or why it was refused. The desk's requests are made
 * from the browser, as monitoring's are: the desk polls every five seconds,
 * and Next runs a page's server actions one at a time.
 */
type Result<K extends string, T> = ({ kind: "ok" } & Record<K, T>) | Refused;

/**
 * Questions of one state of the queue, `page.limit` (at most
 * `QUEUE_MAX_SHOWN`) from the start, or after the clarification `page.after`,
 * with both states' counts. The desk re-reads from the start on every poll,
 * so an answered question leaves the waiting list; past what it keeps live it
 * reads the page after the last item it holds.
 */
export async function fetchQueue(
  contestId: string,
  state: QueueState,
  page: { limit: number; after?: string },
): Promise<Result<"queue", ClarificationQueue>> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (page.after !== undefined && !isId(page.after)) return { kind: "refused", code: "clarification_invalid_cursor" };
  const limit = Math.min(Math.max(1, page.limit), QUEUE_MAX_SHOWN);
  const after = page.after === undefined ? "" : `&after=${page.after}`;
  return browserCall(async () => ({
    kind: "ok" as const,
    queue: clarificationQueueSchema.parse(
      await browserRequest(`/contests/${contestId}/clarifications?state=${state}&limit=${limit}${after}`),
    ),
  }));
}

export type AnswerInput = { body: string; declined: boolean; visibility: Visibility };

/** Answers a waiting question (`answer`), or replaces an answer given (`edit`). */
export async function answerClarification(
  contestId: string,
  clarificationId: string,
  answer: AnswerInput,
  mode: "answer" | "edit",
): Promise<Result<"clarification", StaffClarification>> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (!isId(clarificationId)) return { kind: "refused", code: "clarification_not_found" };
  return browserCall(async () => ({
    kind: "ok" as const,
    clarification: staffClarificationSchema.parse(
      await browserRequest(`/contests/${contestId}/clarifications/${clarificationId}/answer`, {
        method: mode === "answer" ? "POST" : "PUT",
        body: answer,
      }),
    ),
  }));
}

/** One page of the contest's announcements, newest first. */
export async function fetchAnnouncements(contestId: string, offset: number): Promise<Result<"list", AnnouncementList>> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  return browserCall(async () => ({
    kind: "ok" as const,
    list: announcementListSchema.parse(
      await browserRequest(
        `/contests/${contestId}/announcements?limit=${CLARIFICATION_PAGE_SIZE}&offset=${Math.max(0, offset)}`,
      ),
    ),
  }));
}

/** Posts a message to every participant; an empty one is not sent. */
export async function postAnnouncement(contestId: string, body: string): Promise<Result<"announcement", Announcement>> {
  if (!isId(contestId)) return { kind: "refused", code: "invalid_contest_id" };
  if (body.trim() === "") return { kind: "refused", code: "clarification_empty" };
  return browserCall(async () => ({
    kind: "ok" as const,
    announcement: announcementSchema.parse(
      await browserRequest(`/contests/${contestId}/announcements`, { method: "POST", body: { body } }),
    ),
  }));
}


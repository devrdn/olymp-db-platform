import { z } from "zod";

import type { Locale } from "@/lib/i18n/config";

import { CONTEST_STATUSES } from "./contests";

/**
 * The contest archive's wire shapes. Everything here is read by a signed-in
 * visitor of any role, so each schema names the fields it shows and drops the
 * rest (zod's default), and each array has the bound the API itself keeps.
 */

/** The page size the screen asks for; the API's own maximum is 50. */
export const ARCHIVE_PAGE_SIZE = 20;

/** The last page asked for, so the offset stays a safe integer. */
export const ARCHIVE_MAX_PAGE = Math.floor(Number.MAX_SAFE_INTEGER / ARCHIVE_PAGE_SIZE);

/** Characters in a search, as the API counts them (runes). */
export const ARCHIVE_QUERY_MAX = 100;

const archiveItemSchema = z
  .object({
    id: z.string(),
    title: z.string(),
    status: z.enum(CONTEST_STATUSES),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    participants: z.number(),
  })
  .transform((raw) => ({
    id: raw.id,
    title: raw.title,
    status: raw.status,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    participants: raw.participants,
  }));

export type ArchiveItem = z.infer<typeof archiveItemSchema>;

export const archivePageSchema = z.object({
  items: z.array(archiveItemSchema).max(50),
  total: z.number(),
});

const archiveQuestionSchema = z
  .object({
    position: z.number(),
    kind: z.string(),
    points: z.number(),
    is_bonus: z.boolean(),
    title: z.string(),
    body_md: z.string(),
    choice_ids: z.array(z.string()).max(50),
    // Label by choice id; the ids' order is `choice_ids`.
    choices: z.record(z.string(), z.string()).optional(),
  })
  .transform((raw) => ({
    position: raw.position,
    kind: raw.kind,
    points: raw.points,
    isBonus: raw.is_bonus,
    title: raw.title,
    bodyMd: raw.body_md,
    choiceIds: raw.choice_ids,
    choices: raw.choices,
  }));

export type ArchiveQuestion = z.infer<typeof archiveQuestionSchema>;

const archiveTasksSchema = z
  .object({
    lang: z.string(),
    story_md: z.string(),
    schema_notes_md: z.string(),
    questions: z.array(archiveQuestionSchema).max(500),
  })
  .transform((raw) => ({
    lang: raw.lang,
    storyMd: raw.story_md,
    schemaNotesMd: raw.schema_notes_md,
    questions: raw.questions,
  }));

export const archiveContestSchema = z
  .object({
    id: z.string(),
    title: z.string(),
    description: z.string(),
    status: z.enum(CONTEST_STATUSES),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    participants: z.number(),
    // Absent unless the contest's organiser chose to show its tasks.
    tasks: archiveTasksSchema.optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    title: raw.title,
    description: raw.description,
    status: raw.status,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    participants: raw.participants,
    tasks: raw.tasks,
  }));

export type ArchiveContest = z.infer<typeof archiveContestSchema>;

/**
 * The list request for a search and a 1-based page. A page that is not a safe
 * whole number from 1 up is the first, so a hand-edited address cannot ask for
 * a negative or inexact offset, and a page past ARCHIVE_MAX_PAGE is that one.
 */
export function archiveSearch(q: string | undefined, page: number | undefined, locale: Locale): URLSearchParams {
  const search = new URLSearchParams();
  // Cut by characters, not UTF-16 units, so the cut never splits a pair.
  const text = [...(q ?? "").trim()].slice(0, ARCHIVE_QUERY_MAX).join("").trim();
  if (text) search.set("q", text);
  const number = page !== undefined && Number.isSafeInteger(page) && page >= 1 ? Math.min(page, ARCHIVE_MAX_PAGE) : 1;
  search.set("limit", String(ARCHIVE_PAGE_SIZE));
  search.set("offset", String((number - 1) * ARCHIVE_PAGE_SIZE));
  search.set("lang", locale);
  return search;
}

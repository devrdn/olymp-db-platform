import { z } from "zod";

/**
 * The wire shapes of the contest constructor.
 *
 * Parsing at the boundary means a contract change surfaces here, with the
 * field name in the message, instead of as `undefined` three components later.
 * The API speaks snake_case; the interface speaks camelCase, and the mapping
 * happens once, here.
 */

export const CONTEST_STATUSES = ["draft", "published", "running", "finished", "archived"] as const;
export const ENROLLMENTS = ["open", "invite_only"] as const;
export const QUESTION_MODES = ["multi", "single"] as const;
export const TIMINGS = ["fixed", "individual"] as const;

export type ContestStatus = (typeof CONTEST_STATUSES)[number];
export type Enrollment = (typeof ENROLLMENTS)[number];
export type QuestionMode = (typeof QUESTION_MODES)[number];
export type Timing = (typeof TIMINGS)[number];

/** One contest in a listing: a single negotiated title, not every translation. */
export const contestSummarySchema = z
  .object({
    id: z.string(),
    status: z.enum(CONTEST_STATUSES),
    enrollment: z.enum(ENROLLMENTS),
    question_mode: z.enum(QUESTION_MODES),
    lang: z.string(),
    title: z.string(),
    description: z.string().optional(),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
  })
  .transform((raw) => ({
    id: raw.id,
    status: raw.status,
    enrollment: raw.enrollment,
    questionMode: raw.question_mode,
    /** The language the server actually answered in, which may not be the one asked for. */
    lang: raw.lang,
    title: raw.title,
    description: raw.description,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
  }));

export type ContestSummary = z.infer<typeof contestSummarySchema>;

export const contestListSchema = z.object({
  items: z.array(contestSummarySchema),
  total: z.number(),
});

export type ContestList = z.infer<typeof contestListSchema>;

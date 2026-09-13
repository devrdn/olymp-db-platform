import { z } from "zod";

import { ENROLLMENTS, PROGRESSIONS, QUESTION_MODES, SCORINGS, TIMINGS } from "./contests-terms";

export { ENROLLMENTS, PROGRESSIONS, QUESTION_MODES, SCORINGS, TIMINGS } from "./contests-terms";


/**
 * The wire shapes of the contest itself.
 *
 * Parsing at the boundary means a contract change surfaces here, with the
 * field name in the message, instead of as `undefined` three components later.
 * The API speaks snake_case; the interface speaks camelCase, and the mapping
 * happens once, here.
 *
 * The story, the questions and the people have modules of their own. This one
 * covers the contest, its languages, its schedule, and the rules about when it
 * may still be changed.
 */

export const CONTEST_STATUSES = ["draft", "published", "running", "finished", "archived"] as const;
export type ContestStatus = (typeof CONTEST_STATUSES)[number];
export type Enrollment = (typeof ENROLLMENTS)[number];
export type QuestionMode = (typeof QUESTION_MODES)[number];
export type Timing = (typeof TIMINGS)[number];
export type Progression = (typeof PROGRESSIONS)[number];
export type Scoring = (typeof SCORINGS)[number];

/**
 * Which states are reachable from which, mirrored from the Go side.
 *
 * A deliberate second copy, and the only honest option: the alternative is
 * offering every transition and letting the API refuse most of them, which
 * turns the workspace into a guessing game. The API stays the authority — a
 * transition this table allows and the service does not still comes back as
 * `invalid_transition` — so the cost of the two drifting is a button that
 * fails politely, never a state nobody intended.
 *
 * Published → draft exists because publishing is how an author finds out the
 * gate passes; undoing it must not mean deleting the contest.
 */
export const NEXT_STATUSES: Record<ContestStatus, readonly ContestStatus[]> = {
  draft: ["published"],
  published: ["draft", "running", "archived"],
  running: ["finished"],
  finished: ["archived"],
  archived: [],
};

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
    /**
     * Whether the caller is registered for this contest. Always about the
     * caller: the API fills it from the session it authenticated, never from
     * anything the request carries.
     *
     * Defaulted rather than required, so a listing from an older server reads
     * as "not enrolled" instead of failing the whole page — the flag decides
     * which of two labels a row shows, and no screen depends on it to be safe.
     */
    enrolled: z.boolean().default(false),
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
    enrolled: raw.enrolled,
  }));

export type ContestSummary = z.infer<typeof contestSummarySchema>;

export const contestListSchema = z.object({
  items: z.array(contestSummarySchema),
  total: z.number(),
});

export type ContestList = z.infer<typeof contestListSchema>;

/** One language a contest is offered in. Exactly one carries the default. */
export const languageSchema = z
  .object({ code: z.string(), is_default: z.boolean() })
  .transform((raw) => ({ code: raw.code, isDefault: raw.is_default }));

export type ContestLanguage = z.infer<typeof languageSchema>;

/** The authored text in one language. */
export const translationSchema = z.object({
  title: z.string(),
  description: z.string().optional(),
});

export type Translation = z.infer<typeof translationSchema>;

export const settingsSchema = z
  .object({
    enrollment_deadline: z.string().optional(),
    query_rate_limit_per_min: z.number(),
    grace_period_min: z.number(),
  })
  .transform((raw) => ({
    enrollmentDeadline: raw.enrollment_deadline,
    queryRateLimitPerMin: raw.query_rate_limit_per_min,
    gracePeriodMin: raw.grace_period_min,
  }));

export type ContestSettings = z.infer<typeof settingsSchema>;

/**
 * A contest as its staff see it: every translation, not the negotiated one.
 * They are the ones authoring them, and serving a single language would make
 * the rest invisible in the editor.
 */
export const contestSchema = z
  .object({
    id: z.string(),
    status: z.enum(CONTEST_STATUSES),
    enrollment: z.enum(ENROLLMENTS),
    question_mode: z.enum(QUESTION_MODES),
    progression: z.enum(PROGRESSIONS),
    scoring: z.enum(SCORINGS),
    timing: z.enum(TIMINGS),
    duration_min: z.number().nullish(),
    starts_at: z.string().optional(),
    ends_at: z.string().optional(),
    allowed_cidrs: z.array(z.string()),
    settings: settingsSchema,
    leaderboard: z.object({
      freeze_min: z.number().nullable(),
      names: z.enum(["login", "full_name"]),
      revealed_at: z.string().optional(),
    }),
    languages: z.array(languageSchema),
    translations: z.record(z.string(), translationSchema),
    created_at: z.string(),
    updated_at: z.string(),
  })
  .transform((raw) => ({
    id: raw.id,
    status: raw.status,
    enrollment: raw.enrollment,
    questionMode: raw.question_mode,
    progression: raw.progression,
    scoring: raw.scoring,
    timing: raw.timing,
    durationMin: raw.duration_min ?? undefined,
    startsAt: raw.starts_at,
    endsAt: raw.ends_at,
    allowedCidrs: raw.allowed_cidrs,
    settings: raw.settings,
    leaderboard: {
      freezeMin: raw.leaderboard.freeze_min,
      names: raw.leaderboard.names,
      revealedAt: raw.leaderboard.revealed_at,
    },
    languages: raw.languages,
    translations: raw.translations,
    createdAt: raw.created_at,
    updatedAt: raw.updated_at,
  }));

export type Contest = z.infer<typeof contestSchema>;

/**
 * Whether sequential progression (§6.1.1) actually governs this contest.
 *
 * Mirrors contests.Contest.SequentialActive on the Go side (finding 4):
 * progression alone is not enough to ask, since it means nothing at
 * `question_mode = single` — the one question has nothing before it to wait
 * on. Kept here as the one place the interface reads this from, rather than
 * letting the question editor and the settings panel each repeat the
 * two-field comparison and risk reading it differently one day.
 */
export function sequentialActive(contest: Pick<Contest, "progression" | "questionMode">): boolean {
  return contest.progression === "sequential" && contest.questionMode === "multi";
}

/** The language a contest falls back to, or the first one it declares. */
export function defaultLanguage(contest: Contest): string | undefined {
  return (contest.languages.find((l) => l.isDefault) ?? contest.languages[0])?.code;
}

/**
 * The contest's title in the language being read, or in any language it has.
 *
 * The staff response carries every translation and negotiates none, so the
 * choice has to be made here. Falling through to another language is right on
 * an editing screen — the author needs to recognise which contest this is, and
 * a blank heading tells them nothing — but it is wrong in a listing, which is
 * why the summary is negotiated by the server instead.
 */
export function titleIn(contest: Contest, lang: string): string {
  for (const code of [lang, defaultLanguage(contest), ...Object.keys(contest.translations)]) {
    const title = code ? contest.translations[code]?.title : undefined;
    if (title) return title;
  }
  return "";
}

/**
 * Whether the story, the questions and the answers may still change.
 *
 * The line is the start, not the publication: an author publishes to see the
 * contest as participants will, and may still fix a typo. Once it is running,
 * changing a question would change the task under people already answering it.
 *
 * Mirrored from the Go side, with the same standing as the transition table:
 * the API refuses regardless, and this copy exists so the interface can show a
 * disabled control with a reason instead of an enabled one that fails.
 */
export function contentEditable(status: ContestStatus): boolean {
  return status === "draft" || status === "published";
}

/**
 * Whether the contest's own fields may still change.
 *
 * Wider than the content line, deliberately. Extending the window after a
 * power cut and correcting a mistyped network range are exactly what a running
 * contest needs.
 */
export function settingsEditable(status: ContestStatus): boolean {
  return status === "draft" || status === "published" || status === "running";
}

/**
 * Whether the *shape* is still open — the question mode, the timing model, the
 * session length. These freeze with the content, not with the settings:
 * people are already answering under them.
 */
export function shapeEditable(status: ContestStatus): boolean {
  return status === "draft" || status === "published";
}

export const publishProblemSchema = z.object({
  code: z.string(),
  lang: z.string().optional(),
  question_id: z.string().optional(),
  detail: z.string().optional(),
});

export const publishCheckSchema = z.object({
  ready: z.boolean(),
  problems: z.array(publishProblemSchema),
});

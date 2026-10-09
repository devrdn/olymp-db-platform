import { z } from "zod";

import { ENROLLMENTS, PROGRESSIONS, QUESTION_MODES, SCORINGS, TIMINGS } from "./contests-terms";

export { ENROLLMENTS, PROGRESSIONS, QUESTION_MODES, SCORINGS, TIMINGS } from "./contests-terms";


/**
 * The wire shapes of the contest itself: its languages, schedule, and the rules
 * about when it may still change. Parsing at the boundary surfaces a contract
 * change here, with the field name, and maps snake_case to camelCase once.
 * The story, the questions and the people have modules of their own.
 */

/** A contest's lifecycle states. */
export const CONTEST_STATUSES = ["draft", "published", "running", "finished", "archived"] as const;
export type ContestStatus = (typeof CONTEST_STATUSES)[number];
export type Enrollment = (typeof ENROLLMENTS)[number];
export type QuestionMode = (typeof QUESTION_MODES)[number];
export type Timing = (typeof TIMINGS)[number];
export type Progression = (typeof PROGRESSIONS)[number];
export type Scoring = (typeof SCORINGS)[number];

/**
 * Which states are reachable from which, mirrored from the Go side so the
 * workspace offers only valid transitions. The API stays the authority: if the
 * two drift, a button fails with `invalid_transition`.
 *
 * Published → draft exists because publishing is how an author checks the
 * gate; undoing it must not mean deleting the contest.
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
     * Whether the caller is registered, filled from the session, never from the
     * request. Defaulted so an older server reads as "not enrolled" instead of
     * failing the page; no screen depends on it to be safe.
     */
    enrolled: z.boolean().default(false),
    // Read here because a participant may not fetch the staff-only `Contest`
    // (docs/ARCHITECTURE.md §6.1.1).
    scoring: z.enum(SCORINGS),
    icpc_penalty_min: z.number(),
    /**
     * Carried by the listing so the play screen, opened by hundreds of
     * participants at once, needs no second request for its cover. An empty
     * hash means the drawn cover; the default keeps an older server working.
     */
    cover_hash: z.string().default(""),
    cover_attribution: z.string().default(""),
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
    scoring: raw.scoring,
    icpcPenaltyMin: raw.icpc_penalty_min,
    coverHash: raw.cover_hash,
    coverAttribution: raw.cover_attribution,
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

/** A contest as its staff see it: every translation, since they author them all. */
export const contestSchema = z
  .object({
    id: z.string(),
    status: z.enum(CONTEST_STATUSES),
    enrollment: z.enum(ENROLLMENTS),
    question_mode: z.enum(QUESTION_MODES),
    progression: z.enum(PROGRESSIONS),
    scoring: z.enum(SCORINGS),
    // Minutes per wrong attempt on a question later solved. Always present,
    // but meaningful only while `scoring` is `icpc`.
    icpc_penalty_min: z.number(),
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
    // Whether the caller holds contest.monitor here. Only the single-contest
    // read sends it; a write's answer omits it, which reads as "no".
    may_monitor: z.boolean().default(false),
  })
  .transform((raw) => ({
    id: raw.id,
    status: raw.status,
    enrollment: raw.enrollment,
    questionMode: raw.question_mode,
    progression: raw.progression,
    scoring: raw.scoring,
    icpcPenaltyMin: raw.icpc_penalty_min,
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
    mayMonitor: raw.may_monitor,
  }));

export type Contest = z.infer<typeof contestSchema>;

/**
 * A contest's cover metadata, never its bytes. The upload returns the same
 * shape, so the panel shows what was stored without a second read.
 */
export const contestCoverSchema = z
  .object({
    hash: z.string(),
    attribution: z.string(),
    width: z.number(),
    height: z.number(),
  })
  .transform((raw) => ({
    hash: raw.hash,
    attribution: raw.attribution,
    width: raw.width,
    height: raw.height,
  }));

export type ContestCover = z.infer<typeof contestCoverSchema>;

/**
 * Mirrors `covers.MaxAttributionLen`, so the field stops at the limit instead
 * of being refused after the fact.
 */
export const MAX_COVER_ATTRIBUTION = 200;

/**
 * The public address of a cover rendition. The path names the contest, not
 * the file, so the hash in `v` is what busts the cache when the cover is
 * replaced (the API caches a hashed address for a year, a plain one for a
 * minute). 800 is the card; 1600 is the picture above a story.
 */
export function coverHref(contestId: string, hash: string, size: 800 | 1600 = 800): string {
  return `/api/v1/public/contests/${contestId}/cover?size=${size}&v=${hash}`;
}

/**
 * The same rendition read as staff. The public address refuses a draft, which
 * is when a cover is chosen; this one sits behind the session and is answered
 * `private`. The hash still busts its half-minute cache after a replacement.
 */
export function coverStaffHref(contestId: string, hash: string, size: 800 | 1600 = 800): string {
  return `/api/v1/contests/${contestId}/cover/file?size=${size}&v=${hash}`;
}

/**
 * Whether sequential progression (docs/ARCHITECTURE.md §6.1.1) actually
 * governs this contest; mirrors `contests.Contest.SequentialActive`.
 * Progression means nothing at `question_mode = single`.
 */
export function sequentialActive(contest: Pick<Contest, "progression" | "questionMode">): boolean {
  return contest.progression === "sequential" && contest.questionMode === "multi";
}

/** The language a contest falls back to, or the first one it declares. */
export function defaultLanguage(contest: Contest): string | undefined {
  return (contest.languages.find((l) => l.isDefault) ?? contest.languages[0])?.code;
}

/**
 * The title in `lang`, else in any language the contest has. Right for an
 * editing screen, where a blank heading helps nobody; listings use the
 * server-negotiated summary instead.
 */
export function titleIn(contest: Contest, lang: string): string {
  for (const code of [lang, defaultLanguage(contest), ...Object.keys(contest.translations)]) {
    const title = code ? contest.translations[code]?.title : undefined;
    if (title) return title;
  }
  return "";
}

/**
 * Whether the story, the questions and the answers may still change. The line
 * is the start, not publication: once running, a change would alter the task
 * under people answering it. Mirrored from Go so controls can be disabled up
 * front; the API refuses regardless.
 */
export function contentEditable(status: ContestStatus): boolean {
  return status === "draft" || status === "published";
}

/**
 * Whether the contest's own fields may still change. Wider than the content
 * line: a running contest may need its window extended or a network range fixed.
 */
export function settingsEditable(status: ContestStatus): boolean {
  return status === "draft" || status === "published" || status === "running";
}

/**
 * Whether the question mode, timing model and session length may change.
 * They freeze with the content, not the settings.
 */
export function shapeEditable(status: ContestStatus): boolean {
  return status === "draft" || status === "published";
}

/**
 * The submitted ICPC penalty as `icpc_penalty_min`. A disabled (locked) field
 * submits nothing, which means "send no key"; a penalty cannot be unset.
 * Anything but an integer in 0..240 (the column's `CHECK`) is refused, not
 * rounded, so the form never saves a value nobody chose.
 */
export function icpcPenaltyFromForm(
  value: FormDataEntryValue | null,
): { ok: true; value: number | undefined } | { ok: false } {
  if (value === null) return { ok: true, value: undefined };

  const raw = String(value).trim();
  if (!/^\d+$/.test(raw)) return { ok: false };

  const whole = Number(raw);
  if (whole > 240) return { ok: false };
  return { ok: true, value: whole };
}

export function enumFromForm<T extends string>(
  value: FormDataEntryValue | null,
  allowed: readonly T[],
): T | undefined {
  return typeof value === "string" && (allowed as readonly string[]).includes(value)
    ? (value as T)
    : undefined;
}

export type ShapeUpdate = {
  question_mode?: QuestionMode;
  progression?: Progression;
  scoring?: Scoring;
  timing?: Timing;
  duration_min?: number | null;
};

/**
 * The contest's shape as the settings form submitted it. A running contest
 * disables these fieldsets, and a disabled radio group is absent from
 * `FormData`, so each field is sent only when present: defaulting an absent
 * one would ask to change a running contest's shape, and the API refuses the
 * whole `PATCH`.
 *
 * `duration_min` follows `timing`, since its input exists only while
 * "individual" is picked: then a missing or non-positive duration refuses the
 * save; for "fixed" it is sent as `null`.
 */
export function shapeFromForm(
  form: FormData,
): { ok: true; value: ShapeUpdate } | { ok: false } {
  const value: ShapeUpdate = {};

  const questionMode = enumFromForm<QuestionMode>(form.get("questionMode"), QUESTION_MODES);
  if (questionMode !== undefined) value.question_mode = questionMode;

  const progression = enumFromForm<Progression>(form.get("progression"), PROGRESSIONS);
  if (progression !== undefined) value.progression = progression;

  const scoring = enumFromForm<Scoring>(form.get("scoring"), SCORINGS);
  if (scoring !== undefined) value.scoring = scoring;

  const timing = enumFromForm<Timing>(form.get("timing"), TIMINGS);
  if (timing !== undefined) {
    value.timing = timing;
    if (timing === "individual") {
      const duration = Number(form.get("durationMin"));
      if (!Number.isFinite(duration) || duration <= 0) return { ok: false };
      value.duration_min = Math.floor(duration);
    } else {
      value.duration_min = null;
    }
  }

  return { ok: true, value };
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

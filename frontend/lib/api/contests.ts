import { z } from "zod";

import {
  ENROLLMENTS,
  MAX_GRACE_PERIOD_MIN,
  MAX_ICPC_PENALTY_MIN,
  MAX_PRIZE_PLACES,
  PROGRESSIONS,
  QUESTION_MODES,
  SCORINGS,
  TIMINGS,
  VISIBILITIES,
} from "./contests-terms";
import { SQL_MODES } from "./policy-terms";

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
export type Visibility = (typeof VISIBILITIES)[number];
export type QuestionMode = (typeof QUESTION_MODES)[number];
export type Timing = (typeof TIMINGS)[number];
export type Progression = (typeof PROGRESSIONS)[number];
export type Scoring = (typeof SCORINGS)[number];

/**
 * Which states are reachable from which (`contests.Transitions`), so the
 * workspace offers only valid transitions. The API stays the authority.
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
    /**
     * The mode of the contest's SQL policy, which a participant may not read
     * whole; the play screen offers the database reset only under
     * `read_write`. An older server omits it, read as `read_only`.
     */
    sql_mode: z.enum(SQL_MODES).default("read_only"),
    /**
     * Whether the console offers its result as a CSV file. An older server
     * omits it; it offered the download to everybody.
     */
    result_download: z.boolean().default(true),
    /**
     * Whether the schema panel offers the diagram. An older server omits it;
     * the diagram was there for everybody.
     */
    schema_diagram: z.boolean().default(true),
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
    sqlMode: raw.sql_mode,
    resultDownload: raw.result_download,
    schemaDiagram: raw.schema_diagram,
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
    // Defaulted to off for a server older than the limit.
    trusted_domain_only: z.boolean().default(false),
  })
  .transform((raw) => ({
    enrollmentDeadline: raw.enrollment_deadline,
    queryRateLimitPerMin: raw.query_rate_limit_per_min,
    gracePeriodMin: raw.grace_period_min,
    trustedDomainOnly: raw.trusted_domain_only,
  }));

export type ContestSettings = z.infer<typeof settingsSchema>;

/**
 * A contest row's write count: 0 until the row is first written, and kept to
 * what a JavaScript number holds exactly, so it goes back to the API unchanged.
 */
const contestVersionSchema = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER);

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
    // Defaulted for an older server, as in the summary.
    result_download: z.boolean().default(true),
    schema_diagram: z.boolean().default(true),
    // Defaulted to none for a server older than prize places.
    prize_places: z
      .object({ first: z.number(), second: z.number(), third: z.number() })
      .default({ first: 0, second: 0, third: 0 }),
    // Defaulted for a server older than the archive.
    visibility: z.enum(VISIBILITIES).default("open"),
    archive_tasks: z.boolean().default(false),
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
    // Moves on every write of the row, content included; a settings save
    // sends it back and is refused as `contest_changed` once it has moved.
    version: contestVersionSchema,
    // Whether the caller holds contest.monitor here. Only the single-contest
    // read sends it; a write's answer omits it, which reads as "no".
    may_monitor: z.boolean().default(false),
    // The same for contest.clarify, which offers the questions to the organisers.
    may_clarify: z.boolean().default(false),
  })
  .transform((raw) => ({
    id: raw.id,
    status: raw.status,
    enrollment: raw.enrollment,
    questionMode: raw.question_mode,
    progression: raw.progression,
    scoring: raw.scoring,
    icpcPenaltyMin: raw.icpc_penalty_min,
    resultDownload: raw.result_download,
    schemaDiagram: raw.schema_diagram,
    prizePlaces: raw.prize_places,
    visibility: raw.visibility,
    archiveTasks: raw.archive_tasks,
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
    version: raw.version,
    mayMonitor: raw.may_monitor,
    mayClarify: raw.may_clarify,
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

export type PrizePlaces = { first: number; second: number; third: number };

/**
 * The three prize counts as the form submitted them. A blank field is none;
 * anything but a whole number in 0..`MAX_PRIZE_PLACES` refuses the save rather than being
 * rounded into a count nobody chose.
 */
export function prizePlacesFromForm(form: FormData): { ok: true; value: PrizePlaces } | { ok: false } {
  const counts: number[] = [];
  for (const name of ["prizeFirst", "prizeSecond", "prizeThird"]) {
    const raw = String(form.get(name) ?? "").trim();
    if (raw === "") {
      counts.push(0);
      continue;
    }
    if (!/^\d+$/.test(raw) || Number(raw) > MAX_PRIZE_PLACES) return { ok: false };
    counts.push(Number(raw));
  }
  const [first, second, third] = counts;
  return { ok: true, value: { first, second, third } };
}

/**
 * The new grace period as the form submitted it. `current` is the contest's own
 * grace; when it is 0 the installation default is in force, which only the
 * server knows, so any value within the bound passes and the server judges it.
 */
export function graceFromForm(
  form: FormData,
  current: number,
): { ok: true; value: number } | { ok: false; code: "invalid_request" | "grace_not_longer" } {
  const raw = String(form.get("gracePeriodMin") ?? "").trim();
  if (!/^\d+$/.test(raw)) return { ok: false, code: "invalid_request" };

  const value = Number(raw);
  if (value < 1 || value > MAX_GRACE_PERIOD_MIN) return { ok: false, code: "invalid_request" };
  if (current > 0 && value <= current) return { ok: false, code: "grace_not_longer" };

  return { ok: true, value };
}

export type ArchiveSettings = { visibility: Visibility; archiveTasks: boolean };

/**
 * The archive choices as the form submitted them. A visibility outside the
 * list refuses the save; an unticked box submits nothing, which is off.
 */
export function archiveSettingsFromForm(form: FormData): { ok: true; value: ArchiveSettings } | { ok: false } {
  const visibility = enumFromForm<Visibility>(form.get("visibility"), VISIBILITIES);
  if (visibility === undefined) return { ok: false };
  return { ok: true, value: { visibility, archiveTasks: form.get("archiveTasks") === "on" } };
}

/**
 * The submitted ICPC penalty as `icpc_penalty_min`. A disabled (locked) field
 * submits nothing, which means "send no key"; a penalty cannot be unset.
 * Anything but an integer in 0..`MAX_ICPC_PENALTY_MIN` is refused, not
 * rounded, so the form never saves a value nobody chose.
 */
export function icpcPenaltyFromForm(
  value: FormDataEntryValue | null,
): { ok: true; value: number | undefined } | { ok: false } {
  if (value === null) return { ok: true, value: undefined };

  const raw = String(value).trim();
  if (!/^\d+$/.test(raw)) return { ok: false };

  const whole = Number(raw);
  if (whole > MAX_ICPC_PENALTY_MIN) return { ok: false };
  return { ok: true, value: whole };
}

/** The version a settings form was rendered at, or undefined unless it is plain digits the contest schema accepts. */
export function versionFromForm(value: FormDataEntryValue | null): number | undefined {
  if (typeof value !== "string" || !/^\d+$/.test(value)) return undefined;
  const version = contestVersionSchema.safeParse(Number(value));
  return version.success ? version.data : undefined;
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

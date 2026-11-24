/** The vocabulary of a contest's settings, apart from the wire schemas (see content-terms.ts for why). */

import type { ContestStatus } from "./contests";

export const ENROLLMENTS = ["open", "invite_only"] as const;

/** What a finished contest is to everyone but its staff and participants. */
export const VISIBILITIES = ["open", "closed"] as const;

export const QUESTION_MODES = ["multi", "single"] as const;

export const TIMINGS = ["fixed", "individual"] as const;

/**
 * The order questions may be answered in (docs/ARCHITECTURE.md §6.1.1);
 * meaningful only under `question_mode = multi`.
 */
export const PROGRESSIONS = ["free", "sequential"] as const;

/**
 * How a result is derived (docs/ARCHITECTURE.md §6.1.1). Under ICPC, place
 * is solved count then penalty time; question points and percentage
 * penalties stay in the data but are disabled in the editor and never shown
 * to participants.
 */
export const SCORINGS = ["points", "winner", "icpc"] as const;

/** The tag tone of each status; the accent is spent only on what is happening now. */
export const CONTEST_STATUS_TONE: Record<ContestStatus, "live" | "good" | "mute"> = {
  draft: "mute",
  published: "good",
  running: "live",
  finished: "mute",
  archived: "mute",
};

/** Whether the contest is over: finished, or archived since. */
export function hasEnded(status: ContestStatus): boolean {
  return status === "finished" || status === "archived";
}

/**
 * Whether the story, the questions and the answers may still change. The line
 * is the start, not publication: once running, a change would alter the task
 * under people answering it. The question mode, timing model and session
 * length freeze with it. Mirrored from Go so controls can be disabled up
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
  return !hasEnded(status);
}

/**
 * Whether the prize counts may change. They alter only what the standings
 * display, so they stay open after the finish and close with the archive.
 */
export function prizePlacesEditable(status: ContestStatus): boolean {
  return status !== "archived";
}

/** Whether the roster and the activity feed may still move: published, and until the contest ends. */
export function isLiveActivity(status: ContestStatus): boolean {
  return status === "published" || status === "running";
}

/**
 * `covers.MaxAttributionLen`, so the field stops at the limit instead
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

/** `contests.MaxGracePeriodMin`: 90 days. */
export const MAX_GRACE_PERIOD_MIN = 129_600;

/** `contests.MaxPrizePlaces`: the most places one prize may cover. */
export const MAX_PRIZE_PLACES = 1000;

/** `contests.MaxICPCPenaltyMin`: the most minutes one wrong attempt may cost. */
export const MAX_ICPC_PENALTY_MIN = 240;

/** `contests.MaxQuestionTitleRunes`, in characters. */
export const MAX_QUESTION_TITLE_CHARS = 200;

/** `contests.MaxPenaltyPct`: a question's penalty is a percentage. */
export const MAX_PENALTY_PCT = 100;

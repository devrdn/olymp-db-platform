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

/** Mirrors `maxGracePeriodMin` in `backend/internal/contests/contests.go`: 90 days. */
export const MAX_GRACE_PERIOD_MIN = 129_600;

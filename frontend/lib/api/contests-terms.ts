/** The vocabulary of a contest's settings, apart from the wire schemas (see content-terms.ts for why). */

import type { ContestStatus } from "./contests";

export const ENROLLMENTS = ["open", "invite_only"] as const;

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

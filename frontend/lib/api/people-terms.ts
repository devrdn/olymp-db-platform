/** The vocabulary of a roster, apart from the wire schemas (see content-terms.ts for why). */

import type { Participant } from "./people";

/**
 * Whether the participant can be removed rather than disqualified. Once
 * started, their queries and answers are part of the contest's record.
 */
export function removable(participant: Participant): boolean {
  return participant.status === "registered";
}

/**
 * Mirrors `contests.MinDirectoryQueryLength`, the shortest query the trigram
 * indexes can serve. The server answers anything shorter with nothing, so the
 * search box does not ask.
 */
export const MIN_DIRECTORY_QUERY_LENGTH = 3;

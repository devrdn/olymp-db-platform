/**
 * The vocabulary a roster is read with, apart from the schemas that validate the wire.
 *
 * A schema module calls `z.object()` when it loads, so a bundler cannot drop
 * it — and a client component importing one string array from such a module
 * ships the whole of zod with it: 280 KB of parser for a row of buttons. The
 * schemas import these, so a term still has one definition.
 */

import type { Participant } from "./people";

export function removable(participant: Participant): boolean {
  return participant.status === "registered";
}

/**
 * The least a directory search box needs before it is worth asking the
 * server. Mirrors contests.MinDirectoryQueryLength
 * (backend/internal/contests/directory.go), which is set to what
 * migration 000016's trigram indexes actually need — below it PostgreSQL
 * cannot use them and falls back to a sequential scan, so the server always
 * answers a shorter query with nothing. One constant rather than a second
 * copy of the number: a search box that asked at two characters while the
 * server only answers from three would spend a round trip on every such
 * keystroke for an answer it already knows is empty.
 */
export const MIN_DIRECTORY_QUERY_LENGTH = 3;

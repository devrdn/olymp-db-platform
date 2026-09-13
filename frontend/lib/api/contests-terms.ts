/**
 * The vocabulary a contest's own settings are written in, apart from the schemas that validate the wire.
 *
 * A schema module calls `z.object()` when it loads, so a bundler cannot drop
 * it — and a client component importing one string array from such a module
 * ships the whole of zod with it: 280 KB of parser for a row of buttons. The
 * schemas import these, so a term still has one definition.
 */

export const ENROLLMENTS = ["open", "invite_only"] as const;

export const QUESTION_MODES = ["multi", "single"] as const;

export const TIMINGS = ["fixed", "individual"] as const;

/** §6.1.1: the order questions may be answered in. Meaningful only under `question_mode = multi`. */
export const PROGRESSIONS = ["free", "sequential"] as const;

/**
 * §6.1.1: how a result is derived from submissions — points summed, a single
 * winner, or ICPC (docs/superpowers/specs/2026-09-13-icpc-scoring-design.md):
 * place is decided by how many questions are solved and, at a tie, by
 * penalty time. A question's own points and percentage penalty are not used
 * in this mode — they stay in the data (the mode can still be reverted before
 * the contest starts) but the editor disables them, and the participant is
 * never shown a point value.
 */
export const SCORINGS = ["points", "winner", "icpc"] as const;

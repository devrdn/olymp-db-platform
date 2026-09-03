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

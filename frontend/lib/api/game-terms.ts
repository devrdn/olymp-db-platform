/** Game constants for client components, apart from game.ts (see content-terms.ts for why). */

/**
 * Used only when the API sends no `Game.maxScriptBytes`, which is the real
 * ceiling (CLAUDE.md rule 11).
 */
export const FALLBACK_MAX_GAME_SCRIPT_BYTES = 512 * 1024;

/** Poll interval while a build is running. */
export const GAME_POLL_MS = 2000;

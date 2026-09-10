/**
 * Constants about a contest's game that client components need.
 *
 * Apart from game.ts because that module builds its shapes with `zod` at
 * module scope, and importing anything from it into a Client Component drags
 * the whole parser into the browser bundle — the same reason the other
 * `*-terms` modules exist.
 */

/**
 * The largest game script the API will take when it has not said so itself,
 * in bytes.
 *
 * A fallback and nothing more. The real number travels on the game status
 * (`Game.maxScriptBytes`, `game_handler.go`'s own `max_script_bytes`) and is
 * what the editor refuses by; this is only what to use when an older API
 * sent no such field, where refusing nothing at all would be worse than
 * refusing by yesterday's figure. It used to be the only copy, which meant
 * raising the server's ceiling left the editor refusing by the old one with
 * no test on either side to notice.
 */
export const FALLBACK_MAX_GAME_SCRIPT_BYTES = 512 * 1024;

/** How often the interface asks again while a build is running. */
export const GAME_POLL_MS = 2000;

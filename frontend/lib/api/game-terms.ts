/**
 * Constants about a contest's game that client components need.
 *
 * Apart from game.ts because that module builds its shapes with `zod` at
 * module scope, and importing anything from it into a Client Component drags
 * the whole parser into the browser bundle — the same reason the other
 * `*-terms` modules exist.
 */

/**
 * The largest game script the API will take, in bytes — provisioning's own
 * MaxScriptBytes. Held here so the editor can say so before a request is made
 * rather than after one is refused; the server's check is the one that counts.
 */
export const MAX_GAME_SCRIPT_BYTES = 512 * 1024;

/** How often the interface asks again while a build is running. */
export const GAME_POLL_MS = 2000;

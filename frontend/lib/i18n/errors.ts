import type { ApiError } from "@/lib/api/client";

import type { Dictionary } from "./dictionary";

/**
 * Error text comes from the active locale's dictionary, keyed by the machine
 * code the API returns.
 *
 * The server sends an English `message` alongside the code, but it is a
 * developer aid: not translated, not reviewed as product copy, and it may name
 * internals. It never reaches the interface (spec section 8).
 *
 * A code the dictionary does not know means the API grew one this build has
 * not learned yet. A plain sentence beats untranslated English, so the fallback
 * is deliberate rather than defensive.
 */
export function messageForError(failure: ApiError, dict: Dictionary): string {
  const messages = dict.errors as Record<string, string>;
  return messages[failure.code] ?? messages.fallback;
}

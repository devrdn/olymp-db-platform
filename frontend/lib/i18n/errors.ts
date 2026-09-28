import type { Dictionary } from "./dictionary";

/**
 * Error text comes from the active locale's dictionary, keyed by the machine
 * code the API returns.
 *
 * The server sends an English `message` alongside the code, but it is a
 * developer aid: not translated, not reviewed as product copy, and it may name
 * internals. It never reaches the interface.
 *
 * A code the dictionary does not know means the API grew one this build has
 * not learned yet. A plain sentence beats untranslated English, so the fallback
 * is deliberate rather than defensive.
 *
 * Takes the code rather than the error because every caller holds a code: a
 * server action has already turned the failure into `{ code }` by the time a
 * component renders it. And it takes `dict.errors` rather than the whole
 * dictionary so that screens holding a trimmed dictionary can call it too.
 */
export function messageForCode(code: string, errors: Dictionary["errors"]): string {
  return (errors as Record<string, string>)[code] ?? errors.fallback;
}

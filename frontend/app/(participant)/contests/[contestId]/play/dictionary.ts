import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The dictionary sections this screen reads, and nothing else. Whatever
 * `page.tsx` hands a Client Component is serialised into the RSC payload,
 * and the whole `Dictionary` was about half of it, on the screen hundreds of
 * participants open in the same minute. As a type, a component reaching for
 * another section does not compile; widening it is the edit that puts that
 * section on the wire. `lib/i18n/client.tsx` does the same for context.
 */
export type PlayDictionary = Pick<Dictionary, "participant" | "errors" | "leaderboard">;

/** Narrows a whole dictionary to this screen's sections; called on the server. */
export function playDictionary(dict: Dictionary): PlayDictionary {
  return { participant: dict.participant, errors: dict.errors, leaderboard: dict.leaderboard };
}

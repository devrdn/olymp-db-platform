import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The two sections this screen reads, and nothing else (finding 5).
 *
 * `Workspace`, `PlayHeader` and `ReloadLink` are Client Components, so
 * whatever `page.tsx` hands them is serialised into this route's RSC payload
 * — and handing them the whole `Dictionary` put all of it there: measured at
 * 50,727 bytes for `en`, about half the payload, on the one screen hundreds
 * of participants open within the same minute. The play route reads
 * `participant` (4,224 B) and `errors` (12,963 B). The contest workspace's
 * own vocabulary (20,724 B), the account register, the audit trail, the
 * settings and the contest register were the other 31.8 KB, crossing for
 * nobody.
 *
 * A type rather than a convention, so this is checked rather than
 * remembered: every component on this route declares `PlayDictionary`, and a
 * component that reaches for a third section does not compile. Widening it
 * is the same edit that puts the section on the wire, which is exactly where
 * that decision should be visible.
 *
 * `lib/i18n/client.tsx` does the same thing for the client boundaries that
 * read from context; its own doc carries the rest of the reasoning.
 */
export type PlayDictionary = Pick<Dictionary, "participant" | "errors" | "leaderboard">;

/**
 * Narrows a whole dictionary to this screen's own. Called on the server, which is the point.
 *
 * `leaderboard` is the table tab's vocabulary — about 2 KB, and read by the
 * same participants in the same minute as everything else here.
 */
export function playDictionary(dict: Dictionary): PlayDictionary {
  return { participant: dict.participant, errors: dict.errors, leaderboard: dict.leaderboard };
}

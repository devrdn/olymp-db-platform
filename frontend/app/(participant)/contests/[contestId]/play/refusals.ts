import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * What a refusal from the API means for the participant's screen.
 *
 * Every part of this screen that talks to the API — the console, the
 * questions, autosave, the SQL tabs, the events channel, the signal collector
 * and the page itself — has to decide what a refusal means before it can
 * decide what to do: stop for good, wait and try again, show a reference, or
 * just say why. Each used to keep its own list of codes, and the lists had
 * already drifted apart. The facts live here once; what each part does with
 * a fact stays beside that part, because that genuinely differs (the signal
 * collector keeps going outside the contest's network, the events channel
 * does not).
 *
 * - `closed`: the contest is over for this participant. Nothing will be
 *   accepted again.
 * - `dormant`: the contest is not open now, but may be later — it has not
 *   started, or a published contest was taken back to draft and may be
 *   published again. Not over, so nothing stops for good on it; it lifts
 *   when the organiser opens the contest, not on any timer of ours.
 * - `excluded`: the account is not taking part — never was, or was
 *   disqualified. Retrying repeats the refusal.
 * - `elsewhere`: the request came from outside the contest's network. It
 *   lifts if the participant's machine goes back onto it.
 * - `passing`: lifts by itself shortly — a rate limit, a query already
 *   running, the participant's own copy of the game not ready yet. Waiting
 *   is the whole remedy, so it reads quietly.
 * - `fault`: something on our side failed, not the request. The only case
 *   in which a request reference is worth showing, because it is the only
 *   one anybody will be asked to report.
 * - `refused`: everything else — the request itself was refused, and the
 *   dictionary's sentence for its code says what to change.
 *
 * A code is added here only when it is one of the first six; an ordinary
 * refusal needs nothing but its sentence in the dictionaries.
 */
export type RefusalKind =
  | "closed"
  | "dormant"
  | "excluded"
  | "elsewhere"
  | "passing"
  | "fault"
  | "refused";

const KINDS = {
  contest_finished: "closed",
  contest_ended: "closed",
  // The participant's own time is up while the contest may still be running
  // for everyone else: as final for them as the contest ending.
  deadline_passed: "closed",

  contest_not_running: "dormant",

  not_a_participant: "excluded",

  address_not_allowed: "elsewhere",

  query_too_often: "passing",
  query_busy: "passing",
  query_already_running: "passing",
  no_game_yet: "passing",
  answer_too_often: "passing",
  attempt_conflict: "passing",

  internal_error: "fault",
  query_service_down: "fault",
  game_cluster_full: "fault",
  // Invented by this layer, not the API: the API could not be reached at all.
  unreachable: "fault",
} as const satisfies Record<string, Exclude<RefusalKind, "refused">>;

type Named = keyof typeof KINDS;

/** Every code this vocabulary names; its test holds them to the API's contract. */
export const NAMED_CODES = Object.keys(KINDS) as readonly Named[];

/** The codes that mean the contest is over for this participant. */
export type ClosedCode = {
  [Code in Named]: (typeof KINDS)[Code] extends "closed" ? Code : never;
}[Named];

/** What `code` means for this screen; any code not named here is an ordinary refusal. */
export function refusalKind(code: string): RefusalKind {
  return Object.hasOwn(KINDS, code) ? KINDS[code as Named] : "refused";
}

/** Whether `code` means the contest is over for this participant. */
export function isClosed(code: string): code is ClosedCode {
  return refusalKind(code) === "closed";
}

/**
 * Whether a refusal carries the request's reference: a fault, or a code this
 * build has no sentence for (`errors` is the dictionary's own error block).
 * The second is counted as a fault because nobody can say what happened,
 * which is when the reference is the only thing worth quoting. Anything else
 * goes without one: a reference printed under an ordinary refusal reads as
 * though the refusal were a fault.
 */
export function showsReference(code: string, errors: Dictionary["errors"]): boolean {
  return refusalKind(code) === "fault" || !Object.hasOwn(errors, code);
}

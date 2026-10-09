import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * What an API refusal means for the participant's screen, kept in one place
 * for every part that talks to the API. What each part does with the meaning
 * stays beside that part, since it differs (the signal collector keeps going
 * outside the contest's network; the events channel does not).
 *
 * - `closed`: the contest is over for this participant; nothing will be
 *   accepted again.
 * - `dormant`: not open now but may be later (not started, or taken back to
 *   draft). Nothing stops for good; it lifts when the organiser opens the
 *   contest, not on a timer.
 * - `excluded`: the account is not taking part (never was, or disqualified).
 * - `elsewhere`: the request came from outside the contest's network; it
 *   lifts when the machine is back on it.
 * - `passing`: lifts by itself shortly (a rate limit, a query already
 *   running, the game copy not ready). Waiting is the remedy, so it reads
 *   quietly.
 * - `fault`: a failure on our side, the only case worth showing a request
 *   reference for.
 * - `refused`: everything else; the dictionary's sentence says what to change.
 *
 * Only codes of the first six kinds are listed; an ordinary refusal needs
 * only its sentence in the dictionaries.
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
  // The participant's own time is up, while the contest may still run for
  // others: as final for them as the contest ending.
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
  // Made up by this layer: the API could not be reached at all.
  unreachable: "fault",
} as const satisfies Record<string, Exclude<RefusalKind, "refused">>;

type Named = keyof typeof KINDS;

/** Every code named here; its test holds them to the API's contract. */
export const NAMED_CODES = Object.keys(KINDS) as readonly Named[];

/** The codes that mean the contest is over for this participant. */
export type ClosedCode = {
  [Code in Named]: (typeof KINDS)[Code] extends "closed" ? Code : never;
}[Named];

/** What `code` means for this screen; an unnamed code is an ordinary refusal. */
export function refusalKind(code: string): RefusalKind {
  return Object.hasOwn(KINDS, code) ? KINDS[code as Named] : "refused";
}

/** Whether `code` means the contest is over for this participant. */
export function isClosed(code: string): code is ClosedCode {
  return refusalKind(code) === "closed";
}

/**
 * Whether a refusal shows the request reference: for a fault, or for a code
 * this build has no sentence for (`errors` is the dictionary's error block),
 * where the reference is all there is to quote. Under an ordinary refusal it
 * would read as a fault.
 */
export function showsReference(code: string, errors: Dictionary["errors"]): boolean {
  return refusalKind(code) === "fault" || !Object.hasOwn(errors, code);
}

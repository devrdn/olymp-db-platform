import { SIGN_IN_ATTEMPT_WINDOW_MS } from "@/lib/api/accounts-terms";
import {
  ANNOUNCEMENT_MAX_CHARS,
  ANSWER_MAX_CHARS,
  MAX_OPEN_QUESTIONS,
  QUESTION_MAX_CHARS,
} from "@/lib/api/clarifications-terms";
import {
  COVER_MAX_BYTES,
  COVER_MAX_PIXELS,
  MAX_COVER_ATTRIBUTION,
  MAX_GRACE_PERIOD_MIN,
  MAX_ICPC_PENALTY_MIN,
  MAX_PRIZE_PLACES,
} from "@/lib/api/contests-terms";
import { RESULTS_MAX_ROWS } from "@/lib/api/leaderboard-terms";
import {
  FEED_SETTLE_MS,
  IDENTICAL_QUERY_MIN_CHARS,
  LARGE_PASTE_CHARS,
  LONG_ABSENCE_COUNT,
  LONG_ABSENCE_TOTAL_MS,
} from "@/lib/api/monitor-terms";
import { NAME_MAX_CHARS } from "@/lib/api/registrations-terms";
import { FEEDBACK_URL_MAX, IMAGE_MAX_BYTES, IMAGE_MAX_EDGE } from "@/lib/api/settings-terms";
import { NOTES_MAX_CHARS, TAB_TITLE_MAX_CHARS } from "@/lib/api/workspace-terms";
import {
  LOGIN_MAX_CHARS,
  LOGIN_MIN_CHARS,
  PASSWORD_MIN_CHARS,
  SIGNUP_CODE_DIGITS,
} from "@/lib/auth/signup-rules";

import type { Locale } from "./config";

/**
 * The bounds a sentence states, by the placeholder that stands for each. A
 * sentence never spells a bound out: it names the placeholder, and the
 * dictionary is filled from the constant the API's own value is pinned to
 * (lib/api/limits-contract.test.ts), so the two cannot disagree. Each value
 * is in the unit the sentence speaks of.
 */
export const BOUNDS = {
  announcementMaxChars: ANNOUNCEMENT_MAX_CHARS,
  answerMaxChars: ANSWER_MAX_CHARS,
  coverAttributionMax: MAX_COVER_ATTRIBUTION,
  coverMaxMb: COVER_MAX_BYTES / (1024 * 1024),
  coverMaxPixels: COVER_MAX_PIXELS,
  feedSettleSeconds: FEED_SETTLE_MS / 1000,
  feedbackUrlMax: FEEDBACK_URL_MAX,
  gracePeriodMaxDays: MAX_GRACE_PERIOD_MIN / (24 * 60),
  gracePeriodMaxMin: MAX_GRACE_PERIOD_MIN,
  icpcPenaltyMaxMin: MAX_ICPC_PENALTY_MIN,
  identicalQueryMinChars: IDENTICAL_QUERY_MIN_CHARS,
  imageMaxEdge: IMAGE_MAX_EDGE,
  imageMaxKb: IMAGE_MAX_BYTES / 1024,
  largePasteChars: LARGE_PASTE_CHARS,
  longAbsenceCount: LONG_ABSENCE_COUNT,
  longAbsenceMinutes: LONG_ABSENCE_TOTAL_MS / 60_000,
  notesMaxChars: NOTES_MAX_CHARS,
  openQuestionsMax: MAX_OPEN_QUESTIONS,
  passwordMin: PASSWORD_MIN_CHARS,
  prizePlacesMax: MAX_PRIZE_PLACES,
  questionMaxChars: QUESTION_MAX_CHARS,
  registrationNameMax: NAME_MAX_CHARS,
  resultsMaxRows: RESULTS_MAX_ROWS,
  signInLockoutMinutes: SIGN_IN_ATTEMPT_WINDOW_MS / 60_000,
  signupCodeDigits: SIGNUP_CODE_DIGITS,
  signupLoginMax: LOGIN_MAX_CHARS,
  signupLoginMin: LOGIN_MIN_CHARS,
  tabTitleMax: TAB_TITLE_MAX_CHARS,
} as const satisfies Record<string, number>;

const PLACEHOLDER = /\{(\w+)\}/g;

/**
 * The dictionary with every bound placeholder filled, the number written as
 * the locale writes it (grouped from five digits, as Russian and Romanian
 * typography ask). Other placeholders are left for `fill` at render.
 */
export function fillBounds<T>(dictionary: T, locale: Locale): T {
  const format = new Intl.NumberFormat(locale, { useGrouping: "min2" });
  const written = new Map<string, string>(Object.entries(BOUNDS).map(([name, value]) => [name, format.format(value)]));

  const walk = (node: unknown): unknown => {
    if (typeof node === "string") return node.replace(PLACEHOLDER, (whole, name: string) => written.get(name) ?? whole);
    if (Array.isArray(node)) return node.map(walk);
    if (node !== null && typeof node === "object") {
      return Object.fromEntries(Object.entries(node).map(([key, value]) => [key, walk(value)]));
    }
    return node;
  };
  return walk(dictionary) as T;
}

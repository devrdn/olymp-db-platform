import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, test } from "vitest";

import { externalStartHref } from "@/lib/auth/external";
import { MAX_AGE_SECONDS as SIGNUP_EMAIL_MAX_AGE_SECONDS } from "@/lib/auth/signup-email";
import {
  EMAIL_MAX_CHARS,
  FULL_NAME_MAX_BYTES,
  LOGIN_MAX_CHARS,
  PASSWORD_MIN_CHARS,
  PLAIN_LOGIN,
  RESEND_COOLDOWN_SECONDS,
  SIGNUP_CODE_DIGITS,
  isPlainLogin,
} from "@/lib/auth/signup-rules";

import { ACCOUNTS_PAGE, MAX_BULK_ACCOUNTS, MAX_IMPORT_ROWS } from "./accounts-terms";
import { ARCHIVE_PAGE_SIZE, ARCHIVE_QUERY_MAX } from "./archive";
import { AUDIT_PAGE } from "./audit";
import {
  ANNOUNCEMENT_MAX_CHARS,
  ANSWER_MAX_CHARS,
  CLARIFICATION_PAGE_SIZE,
  MAX_OPEN_QUESTIONS,
  QUESTION_MAX_CHARS,
  QUEUE_MAX_SHOWN,
} from "./clarifications-terms";
import { NEXT_STATUSES } from "./contests";
import {
  MAX_COVER_ATTRIBUTION,
  MAX_GRACE_PERIOD_MIN,
  MAX_ICPC_PENALTY_MIN,
  MAX_PENALTY_PCT,
  MAX_PRIZE_PLACES,
  MAX_QUESTION_TITLE_CHARS,
} from "./contests-terms";
import { FALLBACK_MAX_GAME_SCRIPT_BYTES } from "./game-terms";
import { MAX_QUERY_SEARCH } from "./journal-terms";
import { MAX_FEED_PAGE, SIGNAL_BATCH_MAX, SIGNAL_MIN_AWAY_MS, SIGNAL_PASTE_TEXT_MAX } from "./monitor-terms";
import { MIN_DIRECTORY_QUERY_LENGTH } from "./people-terms";
import { QUERY_LOG_PAGE_SIZE } from "./querylog-terms";
import { isResetToken } from "./recovery";
import { EMAIL_MAX_CHARS as REGISTRATION_EMAIL_MAX, NAME_MAX_CHARS } from "./registrations-terms";
import { FEEDBACK_URL_MAX } from "./settings-terms";
import { MAX_TABS, NOTES_MAX_CHARS, TAB_BODY_MAX_BYTES, TAB_TITLE_MAX_CHARS } from "./workspace-terms";

/**
 * The bounds the API generates (`docs/api/limits.json`, from `cmd/apicontract`),
 * so a Go bound changed without its copy here fails this test rather than
 * reaching a person as a refusal the field should have prevented.
 */
type Contract = {
  limits: Record<string, number>;
  durationsMs: Record<string, number>;
  contestTransitions: Record<string, string[]>;
  patterns: Record<string, string>;
};

function contract(): Contract {
  const path = resolve(__dirname, "../../../docs/api/limits.json");
  try {
    return JSON.parse(readFileSync(path, "utf8")) as Contract;
  } catch (cause) {
    throw new Error(`Cannot read ${path}. Regenerate it with \`make api-contract\`.`, { cause });
  }
}

const api = contract();

/** The interface enforces the API's own bound. */
const SAME: Record<string, number> = {
  "archive.MaxQueryRunes": ARCHIVE_QUERY_MAX,
  "clarifications.MaxAnnouncementRunes": ANNOUNCEMENT_MAX_CHARS,
  "clarifications.MaxAnswerRunes": ANSWER_MAX_CHARS,
  "clarifications.MaxOpenPerRegistration": MAX_OPEN_QUESTIONS,
  "clarifications.MaxPageSize": QUEUE_MAX_SHOWN,
  "clarifications.MaxQuestionRunes": QUESTION_MAX_CHARS,
  "contests.MaxGracePeriodMin": MAX_GRACE_PERIOD_MIN,
  "contests.MaxICPCPenaltyMin": MAX_ICPC_PENALTY_MIN,
  "contests.MaxPenaltyPct": MAX_PENALTY_PCT,
  "contests.MaxPrizePlaces": MAX_PRIZE_PLACES,
  "contests.MaxQuestionTitleRunes": MAX_QUESTION_TITLE_CHARS,
  "contests.MinDirectoryQueryLength": MIN_DIRECTORY_QUERY_LENGTH,
  "covers.MaxAttributionLen": MAX_COVER_ATTRIBUTION,
  "monitor.MaxBatchEvents": SIGNAL_BATCH_MAX,
  "monitor.MaxFeedPage": MAX_FEED_PAGE,
  "monitor.MaxPasteTextRunes": SIGNAL_PASTE_TEXT_MAX,
  "monitor.MaxQuerySearchRunes": MAX_QUERY_SEARCH,
  "provisioning.MaxScriptBytes": FALLBACK_MAX_GAME_SCRIPT_BYTES,
  "registrations.MaxEmailBytes": REGISTRATION_EMAIL_MAX,
  "registrations.MaxNameRunes": NAME_MAX_CHARS,
  "settings.MaxFeedbackURLLength": FEEDBACK_URL_MAX,
  "signup.CodeDigits": SIGNUP_CODE_DIGITS,
  "users.MaxBulkAccounts": MAX_BULK_ACCOUNTS,
  "users.MaxEmailLength": EMAIL_MAX_CHARS,
  "users.MaxFullNameLength": FULL_NAME_MAX_BYTES,
  "users.MaxImportRows": MAX_IMPORT_ROWS,
  "users.MinPasswordLength": PASSWORD_MIN_CHARS,
  "workspace.MaxNotesRunes": NOTES_MAX_CHARS,
  "workspace.MaxTabBodyBytes": TAB_BODY_MAX_BYTES,
  "workspace.MaxTabs": MAX_TABS,
  "workspace.MaxTitleRunes": TAB_TITLE_MAX_CHARS,
};

/** A page size the interface chooses and sends as `limit`, which the API clamps to its maximum. */
const AT_MOST: Record<string, number[]> = {
  "archive.MaxPageSize": [ARCHIVE_PAGE_SIZE],
  "audit.MaxPageSize": [AUDIT_PAGE],
  "clarifications.MaxPageSize": [CLARIFICATION_PAGE_SIZE],
  "queryrunner.MaxHistoryLimit": [QUERY_LOG_PAGE_SIZE],
  "users.MaxPageSize": [ACCOUNTS_PAGE],
};

/** Bounds the interface keeps inside a function rather than a constant; their cases are below. */
const BY_BEHAVIOUR = ["connector.MaxReturnPathLength", "recovery.TokenLength"];

/** The durations the interface repeats, in milliseconds. */
const DURATIONS_MS: Record<string, number> = {
  "monitor.MinAway": SIGNAL_MIN_AWAY_MS,
  "signup.CodeLifetime": SIGNUP_EMAIL_MAX_AGE_SECONDS * 1000,
  "signup.ResendCooldown": RESEND_COOLDOWN_SECONDS * 1000,
};

/** The patterns the interface repeats, as their source text. */
const PATTERNS: Record<string, string> = {
  "users.ChosenLoginPattern": PLAIN_LOGIN.source,
};

describe("the limits the API publishes", () => {
  test.each(Object.entries(SAME))("%s is the interface's bound too", (name, value) => {
    expect(value).toBe(api.limits[name]);
  });

  test.each(Object.entries(AT_MOST).flatMap(([name, values]) => values.map((value) => [name, value] as const)))(
    "a page of %s or fewer (%i)",
    (name, value) => {
      expect(api.limits[name]).toBeGreaterThan(0);
      expect(value).toBeLessThanOrEqual(api.limits[name]!);
    },
  );

  // A rule added on the API is checked here, or it would be a copy nothing
  // keeps in step.
  test("each one is checked by a case here", () => {
    const checked = new Set([...Object.keys(SAME), ...Object.keys(AT_MOST), ...BY_BEHAVIOUR]);

    expect(Object.keys(api.limits).filter((name) => !checked.has(name))).toEqual([]);
    expect(Object.keys(api.durationsMs).sort()).toEqual(Object.keys(DURATIONS_MS).sort());
    expect(Object.keys(api.patterns).sort()).toEqual(Object.keys(PATTERNS).sort());
  });

  test("a return path stops where the API's does", () => {
    const at = "/" + "a".repeat(api.limits["connector.MaxReturnPathLength"]! - 1);

    expect(externalStartHref("google", at)).toContain("return=");
    expect(externalStartHref("google", at + "a")).not.toContain("return=");
  });

  test("a reset token is exactly as long as the API's", () => {
    const length = api.limits["recovery.TokenLength"]!;

    expect(isResetToken("a".repeat(length))).toBe(true);
    expect(isResetToken("a".repeat(length - 1))).toBe(false);
    expect(isResetToken("a".repeat(length + 1))).toBe(false);
  });

  test.each(Object.entries(DURATIONS_MS))("%s lasts as long here", (name, value) => {
    expect(value).toBe(api.durationsMs[name]);
  });

  test.each(Object.entries(PATTERNS))("%s is the interface's pattern too", (name, source) => {
    expect(source).toBe(api.patterns[name]);
  });

  test("a chosen login is as long as the field allows", () => {
    expect(isPlainLogin("a".repeat(LOGIN_MAX_CHARS))).toBe(true);
    expect(isPlainLogin("a".repeat(LOGIN_MAX_CHARS + 1))).toBe(false);
  });

  test("a contest moves between the same statuses", () => {
    const sorted = (table: Record<string, readonly string[]>) =>
      Object.fromEntries(Object.entries(table).map(([from, to]) => [from, [...to].sort()]));

    expect(sorted(NEXT_STATUSES)).toEqual(sorted(api.contestTransitions));
  });
});

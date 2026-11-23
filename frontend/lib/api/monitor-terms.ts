/** The vocabulary of the monitoring screens, apart from the wire schemas (see content-terms.ts for why). */

import { API_PREFIX } from "./client";

/** The six flags (docs/ARCHITECTURE.md §9.4), in the order the table shows them. */
export const MONITOR_FLAGS = [
  "multipleIps",
  "parallelSessions",
  "longAbsence",
  "answerWithoutQueries",
  "largePaste",
  "identicalQueries",
] as const;
export type MonitorFlag = (typeof MONITOR_FLAGS)[number];
export type MonitorFlags = Record<MonitorFlag, boolean>;

/** The most events one page of the feed carries (`monitor.MaxFeedPage`). */
export const MAX_FEED_PAGE = 200;

/** The refusal a monitoring screen shows in place of what it read: the organiser's own read budget. */
export const MONITOR_SHOWN = ["monitor_too_often"] as const;

/** The document a notes revision belongs to; any other document is a tab's id. */
export const NOTES_DOCUMENT = "notes";

export function monitorBase(contestId: string): string {
  return `/contests/${encodeURIComponent(contestId)}/monitor`;
}

export function participantBase(contestId: string, registrationId: string): string {
  return `${monitorBase(contestId)}/participants/${encodeURIComponent(registrationId)}`;
}

export function monitorCsvHref(contestId: string): string {
  return `${API_PREFIX}${monitorBase(contestId)}/export.csv`;
}

export function participantCsvHref(contestId: string, registrationId: string): string {
  return `${API_PREFIX}${participantBase(contestId, registrationId)}/export.csv`;
}

import type { FeedDetail, FeedItem, MonitorFlags, RosterRow } from "@/lib/api/monitor";

/**
 * Builders shared by this screen's tests: one participant row and one feed
 * item, each with nothing remarkable about it unless a test says otherwise.
 */

export const NO_FLAGS: MonitorFlags = {
  multipleIps: false,
  parallelSessions: false,
  longAbsence: false,
  answerWithoutQueries: false,
  largePaste: false,
  identicalQueries: false,
};

export function rosterRow(id: string, overrides: Partial<RosterRow> = {}): RosterRow {
  return {
    registrationId: id,
    login: id,
    fullName: `Person ${id}`,
    status: "active",
    startedAt: null,
    finishedAt: null,
    queries: 0,
    queryErrors: 0,
    queryRejected: 0,
    addresses: 1,
    correct: 0,
    wrong: 0,
    pageLeft: 0,
    awayMs: 0,
    pastes: 0,
    ipChanges: 0,
    parallelSessions: 0,
    lastActivity: null,
    flags: { ...NO_FLAGS },
    ...overrides,
  };
}

/** A feed item whose cursor is `cursor`; a query that ended well unless told otherwise. */
export function feedItem(cursor: string, overrides: Partial<FeedItem> = {}): FeedItem {
  const detail: FeedDetail = {
    type: "query",
    id: Number(cursor.replace(/\D/g, "")) || 1,
    sql: `SELECT ${cursor}`,
    sqlTruncated: false,
    status: "ok",
    durationMs: 3,
    rowCount: 1,
  };
  return {
    cursor,
    at: "2026-09-20T10:14:03.120Z",
    kind: "query",
    registrationId: "a",
    login: "a",
    fullName: "Person a",
    detail,
    ...overrides,
  };
}

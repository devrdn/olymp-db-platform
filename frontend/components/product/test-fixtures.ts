import type { LoggedQuery } from "@/lib/api/journal";

/** Builders shared by the query log tests and those built on it. */

/** A successful query with an address, unless overridden. */
export function loggedQuery(id: number, overrides: Partial<LoggedQuery> = {}): LoggedQuery {
  return {
    cursor: `q${id}`,
    executedAt: "2026-09-20T10:14:03.120Z",
    id,
    sql: `SELECT ${id}`,
    sqlTruncated: false,
    status: "ok",
    error: undefined,
    durationMs: 12,
    rowCount: 3,
    ip: "10.0.0.1",
    ...overrides,
  };
}

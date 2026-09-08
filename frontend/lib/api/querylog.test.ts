import { describe, expect, it } from "vitest";

import { queryLogEntrySchema, queryLogResponseSchema } from "./querylog";

describe("the query log wire shape", () => {
  // One page is bounded in bytes as well as in rows, so the server may hand
  // back the beginning of a statement rather than the whole of it. The panel
  // has to be able to tell the two apart: what it shows is the participant's
  // own text, and presenting a shortened copy of it as what they wrote is the
  // one thing this table must not do.
  it("carries whether a row's statement was cut short", () => {
    const cut = queryLogEntrySchema.parse({
      sql: "SELECT 'xxxx",
      sql_truncated: true,
      status: "ok",
      duration_ms: 12,
      row_count: 3,
      executed_at: "2026-03-01T10:00:00Z",
    });

    expect(cut.sqlTruncated).toBe(true);
  });

  // The flag is omitted for a row that was not cut, the way every other
  // "nothing to report" field on this response is. Absent has to read as
  // false, not as undefined the panel then has to guard against.
  it("reads a row with no flag as one that was not cut", () => {
    const whole = queryLogEntrySchema.parse({
      sql: "SELECT 1",
      status: "ok",
      executed_at: "2026-03-01T10:00:00Z",
    });

    expect(whole.sqlTruncated).toBe(false);
    expect(whole.sql).toBe("SELECT 1");
  });

  // `total` counts the log, not the page: it is what the panel compares
  // against how many rows it holds to decide whether to offer "load more",
  // so a page shorter than the total is the ordinary case rather than a
  // contradiction.
  it("keeps a total larger than the page it came with", () => {
    const page = queryLogResponseSchema.parse({
      items: [{ sql: "SELECT 1", status: "ok", executed_at: "2026-03-01T10:00:00Z" }],
      total: 900,
    });

    expect(page.items).toHaveLength(1);
    expect(page.total).toBe(900);
  });
});

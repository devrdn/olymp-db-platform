import { describe, expect, it } from "vitest";

import { queryLogEntryDetailSchema, queryLogEntrySchema, queryLogResponseSchema } from "./querylog";

describe("the query log wire shape", () => {
  // One page is bounded in bytes as well as in rows, so the server may hand
  // back the beginning of a statement rather than the whole of it. The panel
  // has to be able to tell the two apart: what it shows is the participant's
  // own text, and presenting a shortened copy of it as what they wrote is the
  // one thing this table must not do.
  it("carries whether a row's statement was cut short", () => {
    const cut = queryLogEntrySchema.parse({
      id: 1,
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
      id: 1,
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
      items: [{ id: 1, sql: "SELECT 1", status: "ok", executed_at: "2026-03-01T10:00:00Z" }],
      total: 900,
    });

    expect(page.items).toHaveLength(1);
    expect(page.total).toBe(900);
  });

  // §7: every row of the page carries the id a client names back at
  // GET .../play/log/{entryId} to open it in full — query_log.id, a JSON
  // number, not a uuid string.
  it("carries the id a row's own detail request would use", () => {
    const row = queryLogEntrySchema.parse({
      id: 91827,
      sql: "SELECT 1",
      status: "ok",
      executed_at: "2026-03-01T10:00:00Z",
    });

    expect(row.id).toBe(91827);
  });

  // The id is query_log's own bigserial: never zero, never negative, never
  // a fraction. Rejecting these here is the client's own half of the same
  // rule the server's parseLogEntryID enforces on the path parameter.
  it("rejects an id that is not a positive integer", () => {
    for (const bad of [0, -1, 1.5]) {
      expect(() =>
        queryLogEntrySchema.parse({
          id: bad,
          sql: "SELECT 1",
          status: "ok",
          executed_at: "2026-03-01T10:00:00Z",
        }),
      ).toThrow();
    }
  });

  // Number.MAX_SAFE_INTEGER (2^53 - 1) is the schema's own defensive ceiling,
  // not a value any real row reaches — but it is the boundary the schema
  // actually enforces, so it is what a test of that boundary has to use.
  it("accepts an id up to Number.MAX_SAFE_INTEGER and rejects one past it", () => {
    const atTheLimit = queryLogEntrySchema.parse({
      id: Number.MAX_SAFE_INTEGER,
      sql: "SELECT 1",
      status: "ok",
      executed_at: "2026-03-01T10:00:00Z",
    });
    expect(atTheLimit.id).toBe(Number.MAX_SAFE_INTEGER);

    expect(() =>
      queryLogEntrySchema.parse({
        id: Number.MAX_SAFE_INTEGER + 2,
        sql: "SELECT 1",
        status: "ok",
        executed_at: "2026-03-01T10:00:00Z",
      }),
    ).toThrow();
  });

  // GET .../play/log/{entryId} answers with the same shape, sql never cut —
  // queryLogEntryDetailSchema is that same schema, not a second one to keep
  // in sync with it.
  it("parses GET .../play/log/{entryId}'s response with the same schema", () => {
    const detail = queryLogEntryDetailSchema.parse({
      id: 91827,
      sql: "SELECT * FROM suspects WHERE alibi IS NULL",
      status: "ok",
      duration_ms: 42,
      row_count: 7,
      executed_at: "2026-03-01T10:00:00Z",
    });

    expect(detail.id).toBe(91827);
    expect(detail.sqlTruncated).toBe(false);
    expect(detail.sql).toBe("SELECT * FROM suspects WHERE alibi IS NULL");
  });
});

import { describe, expect, it } from "vitest";

import { queryLogEntrySchema, queryLogResponseSchema } from "./querylog";

describe("the query log wire shape", () => {
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

  it("reads a row with no flag as one that was not cut", () => {
    const whole = queryLogEntrySchema.parse({
      sql: "SELECT 1",
      status: "ok",
      executed_at: "2026-03-01T10:00:00Z",
    });

    expect(whole.sqlTruncated).toBe(false);
    expect(whole.sql).toBe("SELECT 1");
  });

  // `total` counts the whole log, not the page.
  it("keeps a total larger than the page it came with", () => {
    const page = queryLogResponseSchema.parse({
      items: [{ sql: "SELECT 1", status: "ok", executed_at: "2026-03-01T10:00:00Z" }],
      total: 900,
    });

    expect(page.items).toHaveLength(1);
    expect(page.total).toBe(900);
  });
});

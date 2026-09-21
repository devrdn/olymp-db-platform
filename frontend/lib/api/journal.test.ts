import { describe, expect, test } from "vitest";

import { answersSchema, queriesSchema } from "./journal";

/**
 * The shapes one participant's record arrives in, which the monitoring
 * routes and the participant's own profile both speak. The two audiences
 * read them through their own modules; what is proved here is the reading
 * itself, once.
 */

const ID = "9a1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

const wireQuery = (id: number, overrides: Record<string, unknown> = {}) => ({
  cursor: `q${id}`,
  executed_at: "2026-09-20T10:14:03.120Z",
  id,
  sql: `SELECT ${id}`,
  sql_truncated: false,
  status: "ok",
  duration_ms: 12,
  row_count: 3,
  ip: "10.0.0.1",
  ...overrides,
});

describe("one participant's record as the API sends it", () => {
  test("reads a page of queries, a failed one with its error and none of the optional fields", () => {
    const parsed = queriesSchema.parse({
      items: [
        wireQuery(9),
        { ...wireQuery(8, { status: "error", error: "division by zero", duration_ms: null, row_count: null }), ip: undefined },
      ],
      more: true,
    });

    expect(parsed.more).toBe(true);
    expect(parsed.items[0]).toEqual({
      cursor: "q9",
      executedAt: "2026-09-20T10:14:03.120Z",
      id: 9,
      sql: "SELECT 9",
      sqlTruncated: false,
      status: "ok",
      error: undefined,
      durationMs: 12,
      rowCount: 3,
      ip: "10.0.0.1",
    });
    expect(parsed.items[1]).toMatchObject({ status: "error", error: "division by zero", durationMs: null, ip: undefined });
  });

  test("reads the answers with the queries that led to each attempt", () => {
    const parsed = answersSchema.parse({
      truncated: false,
      questions: [
        {
          question_id: ID,
          question_ord: 2,
          attempts: [
            {
              id: ID,
              question_id: ID,
              question_ord: 2,
              attempt_no: 1,
              value: "42",
              correct: false,
              points_awarded: 0,
              submitted_at: "2026-09-20T10:20:00.000Z",
              queries: [wireQuery(1)],
              more_queries: 4,
            },
          ],
        },
      ],
    });

    expect(parsed.questions[0].questionOrd).toBe(2);
    expect(parsed.questions[0].attempts[0]).toMatchObject({
      attemptNo: 1,
      value: "42",
      correct: false,
      points: 0,
      submittedAt: "2026-09-20T10:20:00.000Z",
      moreQueries: 4,
    });
    expect(parsed.questions[0].attempts[0].queries[0]).toMatchObject({ id: 1, sql: "SELECT 1" });
  });

});

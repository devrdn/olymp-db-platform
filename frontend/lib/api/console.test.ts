import { describe, expect, it } from "vitest";

import { queryResultSchema } from "./console";

describe("the query result wire shape", () => {
  it("reads a column's type and how long the statement took", () => {
    const parsed = queryResultSchema.parse({
      columns: ["full_name", "at"],
      column_types: ["text", "timestamp with time zone"],
      rows: [["Margot Feilhaber", "2024-11-09 00:14:22"]],
      truncated: false,
      rows_affected: 0,
      duration_micros: 38_000,
    });

    expect(parsed.column_types).toEqual(["text", "timestamp with time zone"]);
    expect(parsed.duration_micros).toBe(38_000);
  });

  // The two lists are read together — the nth type belongs under the nth name
  // — so the schema must carry the type list in the same shape as the names,
  // never as a bare value the caller has to guess the arity of.
  it("keeps the type list in step with the column list", () => {
    const parsed = queryResultSchema.parse({
      columns: ["full_name", "at", "mystery"],
      // A type the runner could not name arrives as an empty entry rather
      // than as a missing one, so the positions still line up.
      column_types: ["text", "timestamp with time zone", ""],
      rows: [],
      truncated: false,
      rows_affected: 0,
      duration_micros: 120,
    });

    expect(parsed.column_types).toHaveLength(parsed.columns.length);
    expect(parsed.column_types?.[2]).toBe("");
  });

  // A runner that has not been upgraded names no types and reports no
  // duration. The console must still draw the table it was given rather than
  // refusing the whole answer over a missing label — and the absence has to
  // stay visible, so that the meter can leave its field blank instead of
  // claiming the query took no time.
  it("accepts an answer from a runner that names neither", () => {
    const parsed = queryResultSchema.parse({
      columns: ["id"],
      rows: [["1"]],
      truncated: false,
      rows_affected: 0,
    });

    expect(parsed.columns).toEqual(["id"]);
    expect(parsed.column_types).toBeUndefined();
    expect(parsed.duration_micros).toBeUndefined();
  });

  // The API never sends null for a list (console_handler.go's own rule), and
  // a client that quietly accepted one would hide the day it did.
  it("refuses a null type list rather than rendering nothing", () => {
    expect(() =>
      queryResultSchema.parse({
        columns: ["id"],
        column_types: null,
        rows: [],
        truncated: false,
        rows_affected: 0,
        duration_micros: 0,
      }),
    ).toThrow();
  });
});

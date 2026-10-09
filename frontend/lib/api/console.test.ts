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

  it("keeps the type list in step with the column list", () => {
    const parsed = queryResultSchema.parse({
      columns: ["full_name", "at", "mystery"],
      column_types: ["text", "timestamp with time zone", ""],
      rows: [],
      truncated: false,
      rows_affected: 0,
      duration_micros: 120,
    });

    expect(parsed.column_types).toHaveLength(parsed.columns.length);
    expect(parsed.column_types?.[2]).toBe("");
  });

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

  // The API never sends null for a list; accepting one would hide the day it did.
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

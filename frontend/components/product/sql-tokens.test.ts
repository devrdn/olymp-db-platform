import { describe, expect, test } from "vitest";

import { tokenizeSql } from "./sql-tokens";

const marked = (sql: string) =>
  tokenizeSql(sql)
    .filter((t) => t.kind !== "plain")
    .map((t) => `${t.kind}:${t.text}`);

describe("reading SQL into highlighted pieces", () => {
  test("names keywords, functions, strings, numbers and comments", () => {
    expect(marked("select count(*) from t where name = 'O''Brien' and n > 4.5e2 -- note\n/* a\nb */")).toEqual([
      "keyword:select",
      "function:count",
      "keyword:from",
      "keyword:where",
      "string:'O''Brien'",
      "keyword:and",
      "number:4.5e2",
      "comment:-- note",
      "comment:/* a\nb */",
    ]);
  });

  test("loses and adds nothing: the pieces are the text", () => {
    const sql = `SELECT "Select", $$it's$$, x1 FROM t1;\n  -- end`;
    expect(
      tokenizeSql(sql)
        .map((t) => t.text)
        .join(""),
    ).toBe(sql);
    expect(marked(sql)).toEqual(["keyword:SELECT", "string:$$it's$$", "keyword:FROM", "comment:-- end"]);
  });

  test("a string or comment left open runs to the end rather than failing", () => {
    expect(marked("SELECT 'open")).toEqual(["keyword:SELECT", "string:'open"]);
    expect(marked("/* open")).toEqual(["comment:/* open"]);
  });
});

import { describe, expect, test } from "vitest";

import { toCsv } from "./csv";

describe("toCsv", () => {
  test("writes the header row from the columns, then one row per record", () => {
    const csv = toCsv(["id", "note"], [["1", "a knife"], ["2", "a rope"]]);

    expect(csv).toBe("id,note\r\n1,a knife\r\n2,a rope\r\n");
  });

  // NULL and the empty string are different in SQL, and the console table
  // keeps them apart — but CSV has no third state for a cell, so a NULL is
  // written as an empty field rather than as the word "null" leaking into a
  // spreadsheet as if it were data.
  test("writes a null as an empty field rather than the word null", () => {
    const csv = toCsv(["alibi"], [[null]]);

    expect(csv).toBe("alibi\r\n\r\n");
  });

  test("quotes a field that contains the delimiter", () => {
    const csv = toCsv(["note"], [["a knife, bloodied"]]);

    expect(csv).toBe('note\r\n"a knife, bloodied"\r\n');
  });

  test("quotes a field that contains a quote, doubling it", () => {
    const csv = toCsv(["note"], [['the "butler" did it']]);

    expect(csv).toBe('note\r\n"the ""butler"" did it"\r\n');
  });

  test("quotes a field that contains a line break", () => {
    const csv = toCsv(["note"], [["line one\nline two"]]);

    expect(csv).toBe('note\r\n"line one\nline two"\r\n');
  });

  test("leaves an ordinary field bare", () => {
    const csv = toCsv(["id"], [["42"]]);

    expect(csv).toBe("id\r\n42\r\n");
  });

  test("writes just the header for a query with no rows", () => {
    const csv = toCsv(["id", "note"], []);

    expect(csv).toBe("id,note\r\n");
  });
});

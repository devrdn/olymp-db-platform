import { describe, expect, test } from "vitest";

import { toCsv } from "./csv";

describe("toCsv", () => {
  test("writes the header row from the columns, then one row per record", () => {
    const csv = toCsv(["id", "note"], [["1", "a knife"], ["2", "a rope"]]);

    expect(csv).toBe("id,note\r\n1,a knife\r\n2,a rope\r\n");
  });

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

  test.each(["=SUM(A1:A2)", "@cmd"])("prefixes a cell beginning %j with an apostrophe", (value) => {
    const csv = toCsv(["formula"], [[value]]);

    expect(csv).toBe(`formula\r\n'${value}\r\n`);
  });

  test("prefixes a cell beginning with a leading tab the same way", () => {
    const csv = toCsv(["formula"], [["\tA1"]]);

    expect(csv).toBe("formula\r\n'\tA1\r\n");
  });

  // A carriage return also forces RFC 4180 quoting.
  test("prefixes a cell beginning with a leading carriage return the same way", () => {
    const csv = toCsv(["formula"], [["\rA1"]]);

    expect(csv).toBe('formula\r\n"\'\rA1"\r\n');
  });

  test.each(["-total", "+HYPERLINK(evil)"])(
    "prefixes a non-numeric cell beginning %j with an apostrophe",
    (value) => {
      const csv = toCsv(["formula"], [[value]]);

      expect(csv).toBe(`formula\r\n'${value}\r\n`);
    },
  );

  // A signed number is data: prefixing it would stop the column summing.
  test.each(["-1", "+1", "-3.14", "-1e10", "-42 "])(
    "leaves a numeric cell beginning with a sign, %j, without a leading apostrophe",
    (value) => {
      const csv = toCsv(["amount"], [[value]]);

      expect(csv).toBe(`amount\r\n${value}\r\n`);
    },
  );

  test("leaves an ordinary cell without a leading apostrophe untouched", () => {
    const csv = toCsv(["id"], [["42"]]);

    expect(csv).toBe("id\r\n42\r\n");
  });

  test("prefixes a formula-looking column name the same way", () => {
    const csv = toCsv(["=col"], [["1"]]);

    expect(csv).toBe("'=col\r\n1\r\n");
  });

  test("still quotes a neutralised field that also contains a comma", () => {
    const csv = toCsv(["note"], [["=A1, bloodied"]]);

    expect(csv).toBe('note\r\n"\'=A1, bloodied"\r\n');
  });
});

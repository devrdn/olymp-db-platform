import { describe, expect, test } from "vitest";

import { quoteIdentifier, tableQuery } from "./table-query";

/** The rule is PostgreSQL's own `quote_ident`, so the statement means the table that was clicked. */
describe("quoteIdentifier", () => {
  test.each([
    ["guests", "guests"],
    ["_ledger2", "_ledger2"],
    // An unreserved keyword is an ordinary name.
    ["name", "name"],
    ["action", "action"],
  ])("leaves a lowercase plain name %s bare", (name, quoted) => {
    expect(quoteIdentifier(name)).toBe(quoted);
  });

  test.each([
    ["Guests", '"Guests"'],
    ["my table", '"my table"'],
    ["2fast", '"2fast"'],
    ["naïve", '"naïve"'],
    ["a$b", '"a$b"'],
    ["", '""'],
  ])("quotes %s, which folding or the scanner would change", (name, quoted) => {
    expect(quoteIdentifier(name)).toBe(quoted);
  });

  test.each([
    ["order", '"order"'],
    ["user", '"user"'],
    ["table", '"table"'],
    // Type and function names, and words a column name cannot be either.
    ["left", '"left"'],
    ["values", '"values"'],
    ["timestamp", '"timestamp"'],
  ])("quotes the keyword %s", (name, quoted) => {
    expect(quoteIdentifier(name)).toBe(quoted);
  });

  test("doubles a quote inside the name", () => {
    expect(quoteIdentifier('say "hi"')).toBe('"say ""hi"""');
  });
});

describe("tableQuery", () => {
  test("reads the first five rows of a table, without running anything", () => {
    expect(tableQuery("guests")).toBe("SELECT * FROM guests LIMIT 5;");
  });

  test("quotes a name that needs it", () => {
    expect(tableQuery("Order Items")).toBe('SELECT * FROM "Order Items" LIMIT 5;');
  });
});

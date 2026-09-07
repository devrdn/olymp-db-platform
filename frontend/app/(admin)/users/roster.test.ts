import { describe, expect, test } from "vitest";

import { parseRoster } from "./roster";

describe("parseRoster", () => {
  test("reads login, full name and email off one comma-separated line each", () => {
    const rows = parseRoster("s.popescu, Sergiu Popescu, s@example.edu\ni.ivanov, Ivan Ivanov");

    expect(rows).toEqual([
      { login: "s.popescu", fullName: "Sergiu Popescu", email: "s@example.edu" },
      { login: "i.ivanov", fullName: "Ivan Ivanov", email: "" },
    ]);
  });

  test("skips a wholly blank line rather than turning it into a row", () => {
    const rows = parseRoster("s.popescu, Sergiu Popescu\n\n   \ni.ivanov, Ivan Ivanov");

    expect(rows).toHaveLength(2);
  });

  test("keeps a bare login as a row with an empty full name, rather than dropping the line", () => {
    // The server refuses this as `invalid_row` — that refusal is what tells
    // whoever pasted the roster which line to fix. Dropping it here instead
    // would make the line vanish with no trace at all.
    const rows = parseRoster("s.popescu");

    expect(rows).toEqual([{ login: "s.popescu", fullName: "", email: "" }]);
  });

  test("reads an empty paste as no rows at all", () => {
    expect(parseRoster("   \n\n  ")).toEqual([]);
  });
});

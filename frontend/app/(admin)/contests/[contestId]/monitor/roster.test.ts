import { describe, expect, test } from "vitest";

import { filterRows, flagCount, mergeRoster, sortRows, widestCells } from "./roster";
import { NO_FLAGS, rosterRow as row } from "./test-fixtures";

/**
 * Reusing unchanged row objects lets memoised rows skip rendering; reusing the
 * array lets the table skip it.
 */
describe("merging a fresh table into the one on screen", () => {
  test("keeps the very same list when nothing changed", () => {
    const previous = [row("a", { queries: 3 }), row("b")];
    const next = [row("a", { queries: 3 }), row("b")];

    expect(mergeRoster(previous, next)).toBe(previous);
  });

  test("keeps every unchanged row and takes the changed one", () => {
    const previous = [row("a"), row("b"), row("c")];
    const changed = row("b", { queries: 1, flags: { ...NO_FLAGS, largePaste: true } });

    const merged = mergeRoster(previous, [row("a"), changed, row("c")]);

    expect(merged).not.toBe(previous);
    expect(merged[0]).toBe(previous[0]);
    expect(merged[1]).toBe(changed);
    expect(merged[2]).toBe(previous[2]);
  });

  test("notices a flag raised on its own", () => {
    const previous = [row("a")];
    const merged = mergeRoster(previous, [row("a", { flags: { ...NO_FLAGS, parallelSessions: true } })]);

    expect(merged).not.toBe(previous);
    expect(merged[0].flags.parallelSessions).toBe(true);
  });

  test("takes a newcomer and drops a row the server no longer sends", () => {
    const previous = [row("a"), row("b")];
    const merged = mergeRoster(previous, [row("a"), row("c")]);

    expect(merged.map((r) => r.registrationId)).toEqual(["a", "c"]);
    expect(merged[0]).toBe(previous[0]);
  });
});

describe("sorting the table", () => {
  const rows = [
    row("a", { fullName: "Boris", queries: 5, awayMs: 10, lastActivity: "2026-09-20T10:00:00.000Z" }),
    row("b", { fullName: "anna", queries: 50, awayMs: 0, lastActivity: null }),
    row("c", { fullName: "Chen", queries: 1, awayMs: 99, lastActivity: "2026-09-20T11:00:00.000Z", flags: { ...NO_FLAGS, largePaste: true, longAbsence: true } }),
  ];

  test("by name, ignoring case", () => {
    expect(sortRows(rows, { key: "name", dir: "asc" }).map((r) => r.registrationId)).toEqual(["b", "a", "c"]);
  });

  test("by a counter, either way", () => {
    expect(sortRows(rows, { key: "queries", dir: "desc" }).map((r) => r.registrationId)).toEqual(["b", "a", "c"]);
    expect(sortRows(rows, { key: "awayMs", dir: "asc" }).map((r) => r.registrationId)).toEqual(["b", "a", "c"]);
  });

  test("by the number of flags", () => {
    expect(sortRows(rows, { key: "flags", dir: "desc" })[0].registrationId).toBe("c");
    expect(flagCount(rows[2])).toBe(2);
  });

  test("by last activity, with nobody-yet at the end either way", () => {
    expect(sortRows(rows, { key: "lastActivity", dir: "desc" }).map((r) => r.registrationId)).toEqual(["c", "a", "b"]);
    expect(sortRows(rows, { key: "lastActivity", dir: "asc" }).map((r) => r.registrationId)).toEqual(["a", "c", "b"]);
  });

  test("does not reorder the list it was given", () => {
    const copy = [...rows];
    sortRows(rows, { key: "queries", dir: "desc" });
    expect(rows).toEqual(copy);
  });
});

describe("filtering the table", () => {
  const rows = [
    row("a", { login: "ivanov", fullName: "Иван Иванов", status: "active", flags: { ...NO_FLAGS, largePaste: true } }),
    row("b", { login: "petrova", fullName: "Anna Petrova", status: "finished" }),
    row("c", { login: "sidorov", fullName: "Pavel Sidorov", status: "disqualified" }),
  ];

  test("to the flagged", () => {
    expect(filterRows(rows, { flaggedOnly: true, status: "", search: "" }).map((r) => r.registrationId)).toEqual(["a"]);
  });

  test("by status", () => {
    expect(filterRows(rows, { flaggedOnly: false, status: "finished", search: "" }).map((r) => r.registrationId)).toEqual(["b"]);
  });

  test("by a piece of the name or the login, ignoring case", () => {
    expect(filterRows(rows, { flaggedOnly: false, status: "", search: "иван" }).map((r) => r.registrationId)).toEqual(["a"]);
    expect(filterRows(rows, { flaggedOnly: false, status: "", search: "PETR" }).map((r) => r.registrationId)).toEqual(["b"]);
    expect(filterRows(rows, { flaggedOnly: false, status: "", search: "  " })).toBe(rows);
  });
});

/** A windowed table sizes its columns from these, not from the rows in view. */
describe("the widest content of each column", () => {
  test("takes the longest name and login, and each counter's largest value", () => {
    const widest = widestCells([
      row("a", { fullName: "Ana", login: "a-very-long-login", queries: 7, awayMs: 1_000 }),
      row("b", { fullName: "Alexandru Constantinescu", login: "ac", queries: 1_204, awayMs: 3_700_000 }),
    ]);

    expect(widest.name).toBe("Alexandru Constantinescu");
    expect(widest.login).toBe("a-very-long-login");
    expect(widest.counters.queries).toBe(1_204);
    expect(widest.counters.awayMs).toBe(3_700_000);
  });

  test("falls back to the login where a row has no name, as the cell does", () => {
    expect(widestCells([row("a", { fullName: "", login: "nameless-but-long" })]).name).toBe("nameless-but-long");
  });

  test("keeps every distinct status and set of flags, in an order that does not depend on the rows'", () => {
    const paste = { ...NO_FLAGS, largePaste: true };
    const both = { ...NO_FLAGS, largePaste: true, multipleIps: true };
    const rows = [
      row("a", { status: "finished", flags: both }),
      row("b", { status: "active", flags: paste }),
      row("c", { status: "active", flags: paste }),
      row("d", { status: "registered" }),
    ];

    const widest = widestCells(rows);
    expect(widest.statuses).toEqual(["active", "finished", "registered"]);
    expect(widest.flagSets).toEqual([paste, both]);
    expect(widestCells([...rows].reverse())).toEqual(widest);
  });

  test("says whether anyone has activity and whether anyone has none", () => {
    expect(widestCells([row("a"), row("b", { lastActivity: "2026-09-20T10:14:03.120Z" })])).toMatchObject({
      activity: "2026-09-20T10:14:03.120Z",
      inactive: true,
    });
    expect(widestCells([row("a", { lastActivity: "2026-09-20T10:14:03.120Z" })]).inactive).toBe(false);
    expect(widestCells([])).toMatchObject({ activity: null, inactive: false, statuses: [], flagSets: [] });
  });
});

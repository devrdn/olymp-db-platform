import { describe, expect, test } from "vitest";

import { filterRows, flagCount, mergeRoster, sortRows } from "./roster";
import { NO_FLAGS, rosterRow as row } from "./test-fixtures";

/**
 * The table is re-read every five seconds and mostly comes back the same.
 * Keeping the previous objects where nothing changed is what lets a memoised
 * row skip its render, and keeping the previous array is what lets the whole
 * table skip it.
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

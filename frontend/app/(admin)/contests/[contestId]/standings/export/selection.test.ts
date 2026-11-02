import { describe, expect, test } from "vitest";

import type { ResultsRow } from "@/lib/api/results";

import {
  allSelection,
  initialSelection,
  keptOnRefresh,
  matchesSearch,
  parsePlace,
  prizeSelection,
  rangeSelection,
  selectedInOrder,
  toggled,
} from "./selection";

function row(id: string, over: Partial<ResultsRow> = {}): ResultsRow {
  return {
    id,
    place: null,
    prize: null,
    fullName: undefined,
    login: undefined,
    email: undefined,
    deleted: false,
    disqualified: false,
    winner: false,
    solved: 0,
    penalty: null,
    points: 0,
    lastSolvedAt: null,
    bonusCredit: null,
    cells: [],
    ...over,
  };
}

// Places 1, 1, 3, 4 and an unplaced row; the disqualified row has no place;
// the prizes sit on the first three.
const rows: ResultsRow[] = [
  row("a", { place: 1, prize: 1, fullName: "Ștefan Popescu", login: "stefan01" }),
  row("b", { place: 1, prize: 1, fullName: "Семён Иванов", login: "ivan01" }),
  row("c", { place: 3, prize: 3, fullName: "Ana Rusu", login: "ana" }),
  row("d", { place: 4, fullName: "Ion Vlad", login: "ion" }),
  row("e", { place: null, fullName: "Unplaced One", login: "unplaced" }),
  row("x", { place: null, disqualified: true, fullName: "Out Of Contest", login: "out" }),
];

const ids = (set: ReadonlySet<string>) => [...set].sort();

describe("initial and full selection", () => {
  test("starts with every row ticked but the disqualified", () => {
    expect(ids(initialSelection(rows))).toEqual(["a", "b", "c", "d", "e"]);
    expect(ids(allSelection(rows))).toEqual(["a", "b", "c", "d", "e"]);
  });

  test("prize winners are the rows with a prize", () => {
    expect(ids(prizeSelection(rows))).toEqual(["a", "b", "c"]);
  });
});

describe("rangeSelection", () => {
  test("a range ticks the places inside it, ties included", () => {
    expect(ids(rangeSelection(rows, 1, 1))).toEqual(["a", "b"]);
    expect(ids(rangeSelection(rows, 3, 4))).toEqual(["c", "d"]);
  });

  test("a range typed backwards means the same span", () => {
    expect(ids(rangeSelection(rows, 3, 1))).toEqual(["a", "b", "c"]);
  });

  test("a range past the last place stops at the last place", () => {
    expect(ids(rangeSelection(rows, 1, 9999))).toEqual(["a", "b", "c", "d"]);
    expect(ids(rangeSelection(rows, 9999, 1))).toEqual(["a", "b", "c", "d"]);
  });

  test("a range with no start begins at 1 and with no end runs to the last place", () => {
    expect(ids(rangeSelection(rows, null, 3))).toEqual(["a", "b", "c"]);
    expect(ids(rangeSelection(rows, 3, null))).toEqual(["c", "d"]);
    expect(ids(rangeSelection(rows, null, null))).toEqual(["a", "b", "c", "d"]);
  });

  test("a range never ticks an unplaced or disqualified row", () => {
    expect(ids(rangeSelection(rows, 1, 9999))).not.toContain("e");
    expect(ids(rangeSelection(rows, 1, 9999))).not.toContain("x");

    const placedButDisqualified = [...rows, row("y", { place: 2, disqualified: true })];
    expect(ids(rangeSelection(placedButDisqualified, 1, 9999))).not.toContain("y");
  });
});

describe("toggled", () => {
  test("a toggle ticks a disqualified row by hand and unticks it again", () => {
    const start = initialSelection(rows);
    const ticked = toggled(start, "x");
    expect(ticked.has("x")).toBe(true);
    expect(toggled(ticked, "x").has("x")).toBe(false);
  });

  test("a toggle returns a new set and leaves the old one alone", () => {
    const start = new Set(["a"]);
    const next = toggled(start, "b");
    expect(next).not.toBe(start);
    expect(ids(start)).toEqual(["a"]);
  });
});

describe("keptOnRefresh", () => {
  test("refresh keeps ticks by registration id and drops ids no longer listed", () => {
    const after = rows.filter((r) => r.id !== "b");
    expect(ids(keptOnRefresh(new Set(["a", "b", "c"]), after))).toEqual(["a", "c"]);
  });

  test("refresh keeps a row ticked that was disqualified since the last load", () => {
    const after = rows.map((r) => (r.id === "c" ? { ...r, place: null, disqualified: true } : r));
    expect(ids(keptOnRefresh(new Set(["a", "c"]), after))).toEqual(["a", "c"]);
  });

  test("refresh does not tick rows that arrived since the last load", () => {
    expect(ids(keptOnRefresh(new Set(["a"]), rows))).toEqual(["a"]);
  });
});

describe("matchesSearch", () => {
  test("search matches a name without its diacritics and a login in any case", () => {
    expect(matchesSearch(rows[0], "stefan")).toBe(true);
    expect(matchesSearch(rows[0], "ȘTEFAN")).toBe(true);
    expect(matchesSearch(rows[1], "IVAN")).toBe(true);
    expect(matchesSearch(rows[1], "Ivan01")).toBe(true);
    expect(matchesSearch(rows[2], "stefan")).toBe(false);
  });

  test("search folds Russian yo to ye in both directions", () => {
    expect(matchesSearch(rows[1], "семен")).toBe(true);
    expect(matchesSearch(row("r", { fullName: "Семен" }), "СЕМЁН")).toBe(true);
  });

  test("search folds the other Romanian letters and the cedilla forms", () => {
    const name = row("r", { fullName: "Țurcanu Mălina Brânduşa Îngrijit" });
    for (const query of ["turcanu", "malina", "branduṣa", "brandusa", "ingrijit", "ŢURCANU"]) {
      expect(matchesSearch(name, query)).toBe(true);
    }
  });

  test("a blank query matches every row and the query is trimmed", () => {
    expect(matchesSearch(rows[0], "")).toBe(true);
    expect(matchesSearch(rows[0], "   ")).toBe(true);
    expect(matchesSearch(rows[0], "  stefan ")).toBe(true);
  });

  test("a row without a name or login matches only the blank query", () => {
    const bare = row("r");
    expect(matchesSearch(bare, "a")).toBe(false);
    expect(matchesSearch(bare, "")).toBe(true);
  });

  test("search does not look at the email", () => {
    expect(matchesSearch(row("r", { fullName: "A", login: "b", email: "zed@example.org" }), "zed")).toBe(false);
  });
});

describe("selectedInOrder", () => {
  test("the request lists the ticked ids in report order", () => {
    expect(selectedInOrder(new Set(["d", "a", "c"]), rows)).toEqual(["a", "c", "d"]);
  });

  test("an id the report does not list is left out", () => {
    expect(selectedInOrder(new Set(["a", "gone"]), rows)).toEqual(["a"]);
  });
});

describe("parsePlace", () => {
  test("parsePlace takes positive integers only", () => {
    expect(parsePlace("3")).toBe(3);
    expect(parsePlace(" 12 ")).toBe(12);
    for (const bad of ["0", "-1", "2.5", "a", "", "  ", "1e2", "3x", "+3"]) {
      expect(parsePlace(bad)).toBeNull();
    }
  });

  test("parsePlace refuses a number too large to be exact", () => {
    expect(parsePlace("99999999999999999999")).toBeNull();
  });
});

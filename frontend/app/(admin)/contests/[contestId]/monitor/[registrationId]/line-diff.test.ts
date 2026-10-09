import { describe, expect, test } from "vitest";

import { collapse, lineDiff, MAX_DIFF_CELLS } from "./line-diff";

const kinds = (before: string, after: string) => lineDiff(before, after).lines.map((l) => `${l.type[0]} ${l.text}`);

describe("a line diff of two revisions", () => {
  test("marks added, removed and unchanged lines in order", () => {
    expect(kinds("a\nb\nc\nd", "a\nc\nx\nd")).toEqual(["s a", "r b", "s c", "a x", "s d"]);
  });

  test("numbers each line on the side it belongs to", () => {
    const { lines, added, removed } = lineDiff("a\nb\nc", "a\nx\nc");
    expect(lines).toEqual([
      { type: "same", text: "a", before: 1, after: 1 },
      { type: "removed", text: "b", before: 2 },
      { type: "added", text: "x", after: 2 },
      { type: "same", text: "c", before: 3, after: 3 },
    ]);
    expect({ added, removed }).toEqual({ added: 1, removed: 1 });
  });

  test("identical bodies are all unchanged", () => {
    const diff = lineDiff("SELECT 1\nFROM t", "SELECT 1\nFROM t");
    expect(diff.lines.every((l) => l.type === "same")).toBe(true);
    expect({ added: diff.added, removed: diff.removed, exact: diff.exact }).toEqual({ added: 0, removed: 0, exact: true });
  });

  test("against an empty previous body every line is added", () => {
    expect(kinds("", "one\ntwo")).toEqual(["a one", "a two"]);
    expect(kinds("one\ntwo", "")).toEqual(["r one", "r two"]);
    expect(lineDiff("", "").lines).toEqual([]);
  });

  test("reads Windows line ends as line ends", () => {
    expect(kinds("a\r\nb", "a\nb")).toEqual(["s a", "s b"]);
  });

  test("keeps the longest common run when a line repeats", () => {
    expect(kinds("x\na\nx", "a\nx\na")).toEqual(["r x", "s a", "s x", "a a"]);
  });

  /**
   * Two maximum-size bodies differing everywhere would need billions of cells;
   * past the bound the diff is marked inexact.
   */
  test("past its bound, shows the changed middle whole instead of the smallest diff", () => {
    const side = Math.ceil(Math.sqrt(MAX_DIFF_CELLS)) + 1;
    const before = ["head", ...Array.from({ length: side }, (_, i) => `b${i}`), "tail"].join("\n");
    const after = ["head", ...Array.from({ length: side }, (_, i) => `a${i}`), "tail"].join("\n");

    const diff = lineDiff(before, after);
    expect(diff.exact).toBe(false);
    expect(diff.lines[0]).toMatchObject({ type: "same", text: "head" });
    expect(diff.lines.at(-1)).toMatchObject({ type: "same", text: "tail" });
    expect(diff.removed).toBe(side);
    expect(diff.added).toBe(side);
  });
});

describe("collapsing unchanged stretches", () => {
  test("keeps the context around each change and counts what it hides", () => {
    const before = Array.from({ length: 20 }, (_, i) => `l${i}`).join("\n");
    const after = before.replace("l10", "changed");
    const shown = collapse(lineDiff(before, after).lines, 2);

    expect(shown.map((row) => (row.type === "skip" ? `skip ${row.count}` : `${row.type[0]} ${row.text}`))).toEqual([
      "skip 8",
      "s l8",
      "s l9",
      "r l10",
      "a changed",
      "s l11",
      "s l12",
      "skip 7",
    ]);
  });

  test("hides nothing when nothing changed is far from a change", () => {
    const lines = lineDiff("a\nb", "a\nc").lines;
    expect(collapse(lines, 3)).toEqual(lines);
  });
});

import { describe, expect, test } from "vitest";

import { OVERSCAN, pieces, rowWindow } from "./row-window";

const ROW = 50;

describe("the rows a scrolled box shows", () => {
  test("starts at the top with the overscan below", () => {
    expect(rowWindow({ scrollTop: 0, clientHeight: 10 * ROW }, ROW)).toEqual({ start: 0, end: 10 + OVERSCAN });
  });

  test("keeps the overscan on both sides further down", () => {
    expect(rowWindow({ scrollTop: 100 * ROW, clientHeight: 10 * ROW }, ROW)).toEqual({
      start: 100 - OVERSCAN,
      end: 110 + OVERSCAN,
    });
  });

  test("counts a partly shown row at either edge", () => {
    expect(rowWindow({ scrollTop: 100 * ROW + 20, clientHeight: 10 * ROW }, ROW)).toEqual({
      start: 100 - OVERSCAN,
      end: 110 + OVERSCAN,
    });
  });

  test("takes off what sits above the first row, such as a held heading", () => {
    expect(rowWindow({ scrollTop: 100 * ROW, clientHeight: 10 * ROW }, ROW, 2 * ROW)).toEqual({
      start: 98 - OVERSCAN,
      end: 108 + OVERSCAN,
    });
    expect(rowWindow({ scrollTop: 0, clientHeight: 10 * ROW }, ROW, 2 * ROW).start).toBe(0);
  });

  test("shows only the overscan from a box with no height yet", () => {
    expect(rowWindow({ scrollTop: 0, clientHeight: 0 }, ROW)).toEqual({ start: 0, end: OVERSCAN });
  });
});

describe("what a windowed list renders", () => {
  const gap = (key: string, rows: number) => ({ kind: "gap", key, rows });
  const rows = (from: number, to: number) => ({ kind: "rows", from, to });

  test("is the window between two gaps when nothing is held", () => {
    expect(pieces({ start: 10, end: 40 }, 2000, -1)).toEqual([gap("before", 10), rows(10, 40), gap("after", 1960)]);
  });

  test("clamps the window to the list", () => {
    expect(pieces({ start: 0, end: 24 }, 5, -1)).toEqual([gap("before", 0), rows(0, 5), gap("after", 0)]);
    expect(pieces({ start: 30, end: 60 }, 20, -1)).toEqual([gap("before", 20), rows(20, 20), gap("after", 0)]);
  });

  test("adds the held row and its neighbours above the window, so Tab and Shift+Tab reach the true next rows", () => {
    expect(pieces({ start: 1000, end: 1030 }, 2000, 3)).toEqual([
      gap("before", 2),
      rows(2, 5),
      gap("under-held", 995),
      rows(1000, 1030),
      gap("after", 970),
    ]);
  });

  test("adds them below the window, on the other side of a gap keyed apart", () => {
    expect(pieces({ start: 0, end: 30 }, 2000, 1999)).toEqual([
      gap("before", 0),
      rows(0, 30),
      gap("over-held", 1968),
      rows(1998, 2000),
      gap("after", 0),
    ]);
  });

  test("joins them to the window when they touch it", () => {
    expect(pieces({ start: 10, end: 40 }, 2000, 41)).toEqual([gap("before", 10), rows(10, 43), gap("after", 1957)]);
    expect(pieces({ start: 10, end: 40 }, 2000, 20)).toEqual([gap("before", 10), rows(10, 40), gap("after", 1960)]);
  });
});

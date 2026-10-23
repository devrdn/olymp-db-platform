/**
 * The windowing both monitoring lists share: rows of one fixed height, so which
 * rows a scrolled box shows is arithmetic rather than measured row by row.
 */

/** Rows rendered past each edge, so a fast scroll or a run of Tab presses meets rendered rows. */
export const OVERSCAN = 8;

/** Rows rendered before the box can be measured, which includes the server's render. */
export const UNMEASURED_ROWS = 24;

export type RowRange = { start: number; end: number };

/** A row height in rem, in pixels at the page's root font size. */
export function remToPx(rem: number): number {
  return rem * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
}

/**
 * The rows `box` shows, plus `OVERSCAN` on each side; `end` is exclusive and
 * not clamped to the list. `offset` is the height inside the scrolled content
 * above the first row, such as a held heading.
 */
export function rowWindow(
  box: { scrollTop: number; clientHeight: number },
  rowHeight: number,
  offset = 0,
): RowRange {
  const first = Math.max(0, Math.floor((box.scrollTop - offset) / rowHeight));
  const fits = Math.ceil(box.clientHeight / rowHeight);
  return { start: Math.max(0, first - OVERSCAN), end: first + fits + OVERSCAN };
}

/** A run of rows to render, `from` inclusive and `to` exclusive, or a spacer standing in for `rows` rows. */
export type Piece = { kind: "rows"; from: number; to: number } | { kind: "gap"; key: string; rows: number };

const gap = (key: string, rows: number): Piece => ({ kind: "gap", key, rows });
const run = (from: number, to: number): Piece => ({ kind: "rows", from, to });

/**
 * What a windowed list of `total` rows renders: the window, and, where they
 * fall outside it, the row at `held` (the one holding focus, or -1) with a
 * neighbour on each side, so Tab and Shift+Tab move to the rows that truly
 * follow and precede it. The gap between that group and the window is keyed
 * by its side, so the held rows' nodes never have to move past it.
 */
export function pieces(range: RowRange, total: number, held: number): Piece[] {
  const start = Math.min(range.start, total);
  const end = Math.max(start, Math.min(range.end, total));
  if (held < 0 || held >= total) return [gap("before", start), run(start, end), gap("after", total - end)];

  const from = Math.max(0, held - 1);
  const to = Math.min(total, held + 2);
  if (to >= start && from <= end) {
    const first = Math.min(start, from);
    const last = Math.max(end, to);
    return [gap("before", first), run(first, last), gap("after", total - last)];
  }
  return from < start
    ? [gap("before", from), run(from, to), gap("under-held", start - to), run(start, end), gap("after", total - end)]
    : [gap("before", start), run(start, end), gap("over-held", from - end), run(from, to), gap("after", total - to)];
}

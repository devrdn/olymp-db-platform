/**
 * What changed between two revisions of a participant's notes or SQL tab,
 * line by line: a longest-common-subsequence diff, small enough to own rather
 * than a dependency for one screen.
 *
 * Bounded. A revision body is at most 80 KB (monitor.MaxRevisionBodyBytes),
 * which can be tens of thousands of lines, and the table below is one cell
 * per pair of lines. The common beginning and end are taken off first — a
 * revision is usually a small edit of the one before — and only the changed
 * middle is compared. When even that is past `MAX_DIFF_CELLS`, the middle is
 * shown as removed and then added, and the result says it is not exact.
 */

/** The most cells the comparison table may have: 2 MB of 16-bit lengths. */
export const MAX_DIFF_CELLS = 1_000_000;

export type DiffLine =
  | { type: "same"; text: string; before: number; after: number }
  | { type: "removed"; text: string; before: number }
  | { type: "added"; text: string; after: number };

export type LineDiff = {
  lines: DiffLine[];
  added: number;
  removed: number;
  /** False when the bound was hit and the middle is shown whole rather than minimally. */
  exact: boolean;
};

function splitLines(body: string): string[] {
  if (body === "") return [];
  return body.replace(/\r\n?/g, "\n").split("\n");
}

export function lineDiff(beforeBody: string, afterBody: string): LineDiff {
  const a = splitLines(beforeBody);
  const b = splitLines(afterBody);
  const lines: DiffLine[] = [];
  let added = 0;
  let removed = 0;

  // The common beginning and end, which need no table.
  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head += 1;
  let tail = 0;
  while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) {
    tail += 1;
  }

  const same = (i: number, j: number) => lines.push({ type: "same", text: a[i], before: i + 1, after: j + 1 });
  const remove = (i: number) => {
    lines.push({ type: "removed", text: a[i], before: i + 1 });
    removed += 1;
  };
  const add = (j: number) => {
    lines.push({ type: "added", text: b[j], after: j + 1 });
    added += 1;
  };

  for (let k = 0; k < head; k += 1) same(k, k);

  const aEnd = a.length - tail;
  const bEnd = b.length - tail;
  const n = aEnd - head;
  const m = bEnd - head;
  const exact = (n + 1) * (m + 1) <= MAX_DIFF_CELLS;

  if (!exact) {
    for (let i = head; i < aEnd; i += 1) remove(i);
    for (let j = head; j < bEnd; j += 1) add(j);
  } else if (n > 0 || m > 0) {
    // table[i][j]: the longest common subsequence of the middles from i and j
    // on. Its values are at most min(n, m), which the bound keeps under
    // 1000, so sixteen bits hold them.
    const width = m + 1;
    const table = new Uint16Array((n + 1) * width);
    for (let i = n - 1; i >= 0; i -= 1) {
      for (let j = m - 1; j >= 0; j -= 1) {
        table[i * width + j] =
          a[head + i] === b[head + j]
            ? table[(i + 1) * width + j + 1] + 1
            : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
      }
    }
    let i = 0;
    let j = 0;
    while (i < n && j < m) {
      if (a[head + i] === b[head + j]) {
        same(head + i, head + j);
        i += 1;
        j += 1;
      } else if (table[(i + 1) * width + j] >= table[i * width + j + 1]) {
        // On a tie the removal goes first: a changed line reads as the old
        // one struck out and then the new one.
        remove(head + i);
        i += 1;
      } else {
        add(head + j);
        j += 1;
      }
    }
    for (; i < n; i += 1) remove(head + i);
    for (; j < m; j += 1) add(head + j);
  }

  for (let k = 0; k < tail; k += 1) same(aEnd + k, bEnd + k);

  return { lines, added, removed, exact };
}

export type DiffRow = DiffLine | { type: "skip"; count: number };

/**
 * The diff as it is shown: `context` unchanged lines around each change, and
 * every longer unchanged stretch as one row that counts it.
 */
export function collapse(lines: readonly DiffLine[], context: number): DiffRow[] {
  const keep = new Uint8Array(lines.length);
  lines.forEach((line, index) => {
    if (line.type === "same") return;
    const from = Math.max(0, index - context);
    const to = Math.min(lines.length - 1, index + context);
    for (let k = from; k <= to; k += 1) keep[k] = 1;
  });

  const rows: DiffRow[] = [];
  let skipped = 0;
  lines.forEach((line, index) => {
    if (keep[index]) {
      if (skipped > 0) rows.push({ type: "skip", count: skipped });
      skipped = 0;
      rows.push(line);
    } else {
      skipped += 1;
    }
  });
  if (skipped > 0) rows.push({ type: "skip", count: skipped });
  return rows;
}

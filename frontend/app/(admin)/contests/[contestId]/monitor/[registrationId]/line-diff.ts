/**
 * Line diff (longest common subsequence) between two revisions of a
 * participant's notes or SQL tab.
 *
 * Bounded: a body can be 80 KB (monitor.MaxRevisionBodyBytes), and the table
 * has one cell per line pair. The common head and tail are stripped first; if
 * the middle still exceeds `MAX_DIFF_CELLS` it is shown as removed then added,
 * marked inexact.
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
  /** False when the bound was hit and the middle is shown whole. */
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

  // The common head and tail need no table.
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
    // table[i][j]: LCS length of the middles from i and j on. At most min(n,
    // m), under 1000 within the bound, so 16 bits suffice.
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
        // On a tie the removal goes first, so a change reads old then new.
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
 * The displayed diff: `context` unchanged lines around each change, and longer
 * unchanged stretches as one counted row.
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

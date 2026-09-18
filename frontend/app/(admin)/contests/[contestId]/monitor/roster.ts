import { MONITOR_FLAGS, type RosterRow } from "@/lib/api/monitor";

/**
 * The participants table's data work, apart from its markup: merging a fresh
 * read into what is on screen, sorting and filtering.
 */

/** The counters a column can be sorted by, beside the name, the status, the flags and the last activity. */
export const COUNTER_KEYS = [
  "queries",
  "queryErrors",
  "queryRejected",
  "correct",
  "wrong",
  "pageLeft",
  "awayMs",
  "pastes",
  "ipChanges",
  "parallelSessions",
] as const;
export type CounterKey = (typeof COUNTER_KEYS)[number];

export type SortKey = "name" | "status" | "flags" | "lastActivity" | CounterKey;
export type SortOrder = { key: SortKey; dir: "asc" | "desc" };

export type RosterFilter = {
  flaggedOnly: boolean;
  /** A registration status, or "" for every one. */
  status: string;
  search: string;
};

/** How many of the six flags a row carries. */
export function flagCount(row: RosterRow): number {
  let count = 0;
  for (const flag of MONITOR_FLAGS) if (row.flags[flag]) count += 1;
  return count;
}

const SCALARS = [
  "login",
  "fullName",
  "status",
  "startedAt",
  "finishedAt",
  "addresses",
  "lastActivity",
  ...COUNTER_KEYS,
] as const satisfies readonly (keyof RosterRow)[];

function sameRow(a: RosterRow, b: RosterRow): boolean {
  for (const key of SCALARS) if (a[key] !== b[key]) return false;
  for (const flag of MONITOR_FLAGS) if (a.flags[flag] !== b.flags[flag]) return false;
  return true;
}

/**
 * The fresh table, reusing whatever is unchanged from the one on screen.
 *
 * The table is read every five seconds and mostly comes back as it was. An
 * unchanged row keeps its previous object, so its memoised component skips
 * the render; a read where nothing changed at all hands back the previous
 * array itself, so the state does not move and nothing renders.
 */
export function mergeRoster(previous: readonly RosterRow[], next: readonly RosterRow[]): RosterRow[] {
  const byId = new Map(previous.map((row) => [row.registrationId, row]));
  let changed = previous.length !== next.length;
  const merged = next.map((row, index) => {
    const old = byId.get(row.registrationId);
    if (old && sameRow(old, row)) {
      if (previous[index] !== old) changed = true;
      return old;
    }
    changed = true;
    return row;
  });
  return changed ? merged : (previous as RosterRow[]);
}

const collator = new Intl.Collator(undefined, { sensitivity: "base", numeric: true });

function label(row: RosterRow): string {
  return row.fullName || row.login;
}

/** A sorted copy; ties fall back to the name, so the order is stable between reads. */
export function sortRows(rows: readonly RosterRow[], order: SortOrder): RosterRow[] {
  const sign = order.dir === "asc" ? 1 : -1;
  const byName = (a: RosterRow, b: RosterRow) =>
    collator.compare(label(a), label(b)) || a.registrationId.localeCompare(b.registrationId);

  const compare = (a: RosterRow, b: RosterRow): number => {
    switch (order.key) {
      case "name":
        return sign * byName(a, b);
      case "status":
        return sign * a.status.localeCompare(b.status) || byName(a, b);
      case "flags":
        return sign * (flagCount(a) - flagCount(b)) || byName(a, b);
      case "lastActivity": {
        // Nobody-yet goes last whichever way: an empty cell at the top of a
        // "most recent" sort is an answer to a question nobody asked.
        if (a.lastActivity === b.lastActivity) return byName(a, b);
        if (!a.lastActivity) return 1;
        if (!b.lastActivity) return -1;
        return sign * (a.lastActivity < b.lastActivity ? -1 : 1);
      }
      default:
        return sign * (a[order.key] - b[order.key]) || byName(a, b);
    }
  };
  return [...rows].sort(compare);
}

/** The rows the filter keeps; the same list when it keeps them all. */
export function filterRows(rows: RosterRow[], filter: RosterFilter): RosterRow[] {
  const needle = filter.search.trim().toLocaleLowerCase();
  if (!filter.flaggedOnly && !filter.status && !needle) return rows;
  return rows.filter(
    (row) =>
      (!filter.flaggedOnly || flagCount(row) > 0) &&
      (!filter.status || row.status === filter.status) &&
      (!needle ||
        row.fullName.toLocaleLowerCase().includes(needle) ||
        row.login.toLocaleLowerCase().includes(needle)),
  );
}

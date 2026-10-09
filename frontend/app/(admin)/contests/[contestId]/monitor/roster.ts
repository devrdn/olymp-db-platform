import { MONITOR_FLAGS, type RosterRow } from "@/lib/api/monitor";

/** The participants table's data work: merging fresh reads, sorting and filtering. */

/** Counter columns that can be sorted, besides name, status, flags and last activity. */
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
  /** A registration status, or "" for all. */
  status: string;
  search: string;
};

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
 * Merges a fresh read, reusing unchanged row objects so memoised rows skip
 * rendering; if nothing changed the previous array itself is returned.
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
        // No activity yet sorts last in either direction.
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

/** The filtered rows; the same list when all are kept. */
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

import type { ResultsRow } from "@/lib/api/results";

/**
 * Which registrations a results export carries. Every function returns a new
 * set; a row's place decides a range, its id decides everything else, so a
 * tick survives a refresh that reorders or re-ranks the list.
 */

/** Everyone but the disqualified: the default, and what "All" ticks. */
export function initialSelection(rows: readonly ResultsRow[]): Set<string> {
  return new Set(rows.filter((row) => !row.disqualified).map((row) => row.id));
}

export function allSelection(rows: readonly ResultsRow[]): Set<string> {
  return initialSelection(rows);
}

export function prizeSelection(rows: readonly ResultsRow[]): Set<string> {
  return new Set(rows.filter((row) => row.prize !== null).map((row) => row.id));
}

/**
 * The placed, non-disqualified rows whose place lies between the two bounds in
 * either order. A missing start is place 1 and a missing end is unbounded, so
 * a bound past the last place stops there.
 */
export function rangeSelection(
  rows: readonly ResultsRow[],
  from: number | null,
  to: number | null,
): Set<string> {
  const a = from ?? 1;
  const b = to ?? Number.POSITIVE_INFINITY;
  const low = Math.min(a, b);
  const high = Math.max(a, b);
  return new Set(
    rows
      .filter((row) => row.place !== null && !row.disqualified && row.place >= low && row.place <= high)
      .map((row) => row.id),
  );
}

export function toggled(selected: ReadonlySet<string>, id: string): Set<string> {
  const next = new Set(selected);
  if (!next.delete(id)) next.add(id);
  return next;
}

/** Ticks follow the registration, not its standing: a row disqualified since the last load stays ticked. */
export function keptOnRefresh(selected: ReadonlySet<string>, rows: readonly ResultsRow[]): Set<string> {
  const listed = new Set(rows.map((row) => row.id));
  return new Set([...selected].filter((id) => listed.has(id)));
}

// Marks stripped after decomposition: Romanian comma-below and cedilla forms
// (ș ş ț ţ), breve, circumflex, and the diaeresis that separates ё from е.
function fold(text: string): string {
  return text.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
}

/** Trimmed, case- and diacritic-insensitive substring of the name or the login; blank matches all. */
export function matchesSearch(row: ResultsRow, query: string): boolean {
  const needle = fold(query.trim());
  if (needle === "") return true;
  return fold(row.fullName ?? "").includes(needle) || fold(row.login ?? "").includes(needle);
}

/** The ticked ids in report order, which is the order the export lists them. */
export function selectedInOrder(selected: ReadonlySet<string>, rows: readonly ResultsRow[]): string[] {
  return rows.filter((row) => selected.has(row.id)).map((row) => row.id);
}

/** A positive integer, else null: what a place field may hold. */
export function parsePlace(text: string): number | null {
  const trimmed = text.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const value = Number(trimmed);
  return Number.isSafeInteger(value) && value > 0 ? value : null;
}

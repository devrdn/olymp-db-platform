/**
 * Where a participant moved the schema diagram's tables, remembered in this
 * browser per contest. Only moved tables are kept; every other table stays
 * where the automatic layout puts it. The positions belong to one set of
 * tables: a schema rebuilt with other tables starts from the layout again.
 * Storage may be refused (a private window, a locked-down lab machine), and
 * then the positions last only while the diagram is open.
 */

export type Position = { x: number; y: number };
export type Positions = ReadonlyMap<string, Position>;

const VERSION = 1;
/** Far past any layout of the API's 200 tables; a larger number was not written by this code. */
const MAX_COORDINATE = 1_000_000;

function storageKey(contestId: string) {
  return `dbcontest.diagram.${contestId}`;
}

/** A short fingerprint of the set of table names, whatever their order. */
export function schemaIdentity(names: readonly string[]): string {
  // FNV-1a over the sorted names joined by NUL, which no identifier contains.
  let hash = 0x811c9dc5;
  for (const char of [...names].sort().join("\u0000")) {
    hash ^= char.codePointAt(0)!;
    hash = Math.imul(hash, 0x01000193);
  }
  return `${names.length}.${(hash >>> 0).toString(36)}`;
}

export function readPositions(contestId: string, names: readonly string[]): Positions {
  const positions = new Map<string, Position>();
  let stored: unknown;
  try {
    stored = JSON.parse(window.localStorage.getItem(storageKey(contestId)) ?? "null");
  } catch {
    return positions;
  }
  if (!isRecord(stored) || stored.v !== VERSION || stored.schema !== schemaIdentity(names)) return positions;
  if (!isRecord(stored.tables)) return positions;

  const known = new Set(names);
  for (const [name, value] of Object.entries(stored.tables)) {
    if (known.has(name) && Array.isArray(value) && value.length === 2 && value.every(isCoordinate)) {
      positions.set(name, { x: value[0], y: value[1] });
    }
  }
  return positions;
}

export function writePositions(contestId: string, names: readonly string[], positions: Positions) {
  try {
    if (positions.size === 0) {
      window.localStorage.removeItem(storageKey(contestId));
      return;
    }
    const tables = Object.fromEntries([...positions].map(([name, { x, y }]) => [name, [x, y]]));
    window.localStorage.setItem(
      storageKey(contestId),
      JSON.stringify({ v: VERSION, schema: schemaIdentity(names), tables }),
    );
  } catch {
    // Refused or full: the positions still hold until the diagram closes.
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isCoordinate(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && Math.abs(value) <= MAX_COORDINATE;
}

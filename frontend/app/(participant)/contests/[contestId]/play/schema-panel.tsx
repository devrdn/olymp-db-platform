"use client";

import { useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

import { cn } from "@/lib/utils";
import type { GameSchema, GameTable } from "@/lib/api/schema";
import type { PlayDictionary } from "./dictionary";

/**
 * The console's schema panel: the game's tables, their columns, each column's
 * type and the table a foreign key points at. A permanent column because it
 * is read while typing. Absent, not empty, when the organiser closed the
 * catalogues, where discovering the shape is the puzzle
 * (queryproxy.ErrSchemaHidden).
 */
export function SchemaPanel({
  schema,
  hidden = false,
  onReveal,
  dict,
}: {
  schema: GameSchema;
  /**
   * Collapsed by the participant. Still mounted, so the search and the
   * open tables survive; only ⌘K has to reveal it first.
   */
  hidden?: boolean;
  /** Asks the screen to show this panel again; called by ⌘K while collapsed. */
  onReveal?: () => void;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.schema;
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  // The filter runs one render behind the typed value, so typing stays
  // within SPEC.md §6's 120ms even at the API's bound of 200 tables of 200
  // columns.
  const deferred = useDeferredValue(query);

  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => initiallyCollapsed(schema.tables));

  /** ⌘K arrived while collapsed: focus the field once it is shown again. */
  const focusWhenShown = useRef(false);

  // Read through refs so the document listener is bound once and still sees
  // the current props.
  const hiddenRef = useRef(hidden);
  const onRevealRef = useRef(onReveal);
  useEffect(() => {
    hiddenRef.current = hidden;
    onRevealRef.current = onReveal;
  });

  // ⌘K, bound on the document so it works from the editor. While collapsed
  // the panel asks to be shown first; focusing inside a `display:none`
  // subtree does nothing, so the intent is spent in the layout effect below
  // once the panel is back.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key.toLowerCase() !== "k" || !(event.metaKey || event.ctrlKey)) return;
      event.preventDefault();
      if (hiddenRef.current) {
        focusWhenShown.current = true;
        onRevealRef.current?.();
        return;
      }
      searchRef.current?.focus();
      searchRef.current?.select();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);


  useLayoutEffect(() => {
    if (hidden || !focusWhenShown.current) return;
    focusWhenShown.current = false;
    searchRef.current?.focus();
    searchRef.current?.select();
  }, [hidden]);

  const matches = useMemo(() => filterTables(schema.tables, deferred), [schema.tables, deferred]);

  return (
    <section aria-label={t.heading} className="flex min-h-0 flex-1 flex-col">
      <header className="flex shrink-0 items-center justify-between gap-2 border-b border-line px-3 py-2 font-mono text-label text-ink-3 uppercase">
        <span>
          {t.heading} · {t.language}
        </span>
        <span>{schema.tables.length}</span>
      </header>

      {/* `relative` so this scroll box is the containing block of the
          `sr-only` label (`position: absolute`), as in SidePanel. */}
      <div className="relative min-h-0 flex-1 overflow-y-auto py-2.5">
        <div className="px-3 pb-2">
          <label className="sr-only" htmlFor="schema-search">
            {t.searchLabel}
          </label>
          <div className="flex items-center gap-2 rounded-full border border-line-2 px-3 py-1">
            <input
              id="schema-search"
              ref={searchRef}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t.search}
              className="min-w-0 flex-1 bg-transparent font-mono text-data text-ink outline-none placeholder:text-ink-3"
            />
            {/* Decorative: reading "command K" after the placeholder is noise. */}
            <span aria-hidden="true" className="shrink-0 font-mono text-label text-ink-3">
              ⌘K
            </span>
          </div>
        </div>

        {schema.truncated ? (
          <p className="px-3 pb-2 font-mono text-label text-warn normal-case">{t.truncated}</p>
        ) : null}

        {schema.tables.length === 0 ? (
          <p className="px-3 font-mono text-data text-ink-3">{t.empty}</p>
        ) : matches.length === 0 ? (
          <p className="px-3 font-mono text-data text-ink-3">{t.nothingFound}</p>
        ) : (
          <ul className="font-mono text-data">
            {matches.map((table) => {
              // A search opens the tables it matched inside: a hidden hit is no hit.
              const open = deferred.trim() !== "" ? table.matchedColumns : !collapsed.has(table.name);
              return (
                <TableRow
                  key={table.name}
                  table={table}
                  open={open}
                  onToggle={() =>
                    setCollapsed((current) => {
                      const next = new Set(current);
                      if (next.has(table.name)) next.delete(table.name);
                      else next.add(table.name);
                      return next;
                    })
                  }
                  t={t}
                />
              );
            })}
          </ul>
        )}
      </div>
    </section>
  );
}

/** One table and, when it is open, its columns. */
function TableRow({
  table,
  open,
  onToggle,
  t,
}: {
  table: MatchedTable;
  open: boolean;
  onToggle: () => void;
  t: PlayDictionary["participant"]["play"]["schema"];
}) {
  const foreignKeys = table.columns.filter((column) => column.references !== "").length;

  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className={cn(
          "flex w-full items-center gap-2 px-3 text-left text-ink",
          "hover:bg-sunk",
          open && "bg-accent-wash font-medium text-accent",
        )}
      >
        <span aria-hidden="true" className="shrink-0 text-ink-3">
          {open ? "▾" : "▸"}
        </span>
        <span className="truncate">{table.name}</span>
        {foreignKeys > 0 ? (
          <span className="ml-auto shrink-0 font-mono text-label text-ink-3 normal-case">
            fk {foreignKeys}
          </span>
        ) : null}
      </button>

      {open ? (
        <ul>
          {table.columns.map((column) => (
            <li key={column.name} className="flex items-baseline gap-2 py-px pr-3 pl-7 text-ink-2">
              <span className="truncate" title={column.name}>
                {column.name}
              </span>
              {/* Shrinkable, three to one in the name's favour: the name is what
                  the participant types, while the type can be read from the title.
                  `truncate` keeps a long type such as `timestamp with time zone`
                  from overflowing the row. */}
              <span
                className="ml-auto min-w-0 shrink-3 truncate font-mono text-label text-ink-3 normal-case"
                // The visible text first, since it may be cut off, then the note
                // (nullable, or the foreign key's target), joined with the heading's
                // interpunct so nothing new needs translating.
                title={[
                  column.references !== "" ? `fk ${column.references}` : column.type,
                  column.references !== ""
                    ? t.foreignKey.replace("{table}", column.references)
                    : column.nullable
                      ? t.nullable
                      : "",
                ]
                  .filter((part) => part !== "")
                  .join(" · ")}
              >
                {column.references !== "" ? `fk ${column.references}` : column.type}
              </span>
            </li>
          ))}
        </ul>
      ) : null}
    </li>
  );
}

/** A table narrowed to what the search matched, and whether it matched. */
type MatchedTable = GameTable & { matchedColumns: boolean };

/**
 * Which tables start collapsed: none in a small schema, so the columns are
 * visible without clicking; all of them past 200 rows, since laying out up to
 * forty thousand rows at once blows the frame budget.
 */
function initiallyCollapsed(tables: readonly GameTable[]): ReadonlySet<string> {
  const rows = tables.reduce((total, table) => total + table.columns.length + 1, 0);
  return rows <= 200 ? new Set<string>() : new Set(tables.map((table) => table.name));
}

/**
 * Tables matching the search. A table matched by name keeps all its columns;
 * one matched by its columns is narrowed to them.
 */
function filterTables(tables: readonly GameTable[], query: string): MatchedTable[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") return tables.map((table) => ({ ...table, matchedColumns: false }));

  const result: MatchedTable[] = [];
  for (const table of tables) {
    if (table.name.toLowerCase().includes(needle)) {
      result.push({ ...table, matchedColumns: true });
      continue;
    }
    const columns = table.columns.filter(
      (column) =>
        column.name.toLowerCase().includes(needle) ||
        column.type.toLowerCase().includes(needle) ||
        column.references.toLowerCase().includes(needle),
    );
    if (columns.length > 0) result.push({ ...table, columns, matchedColumns: true });
  }
  return result;
}

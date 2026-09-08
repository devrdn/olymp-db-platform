"use client";

import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";

import { cn } from "@/lib/utils";
import type { GameSchema, GameTable } from "@/lib/api/schema";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * The console's schema panel: the tables of the game, their columns, each
 * column's type and the table a foreign key points at.
 *
 * The left column of the design's console (docs/design/preview.html,
 * "SQL-консоль"). It is the one part of that screen that is pure reference —
 * nothing here submits anything — and the reason it earns a permanent column
 * rather than a tab is that it is read *while* typing the query beside it.
 *
 * The panel is absent, not empty, in a contest whose organiser closed the
 * catalogues: `page.tsx` never passes a schema in that case, because
 * discovering the shape is the puzzle there (queryproxy.ErrSchemaHidden).
 */
export function SchemaPanel({ schema, dict }: { schema: GameSchema; dict: Dictionary }) {
  const t = dict.participant.play.schema;
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  // The filter runs against the typed value one render behind, so a keystroke
  // is never waiting on it. A game this product is for has seven tables and
  // would not notice; the API's own bound allows two hundred tables of two
  // hundred columns, and this is what keeps the panel's typing at the 120ms
  // SPEC.md §6 asks for even there.
  const deferred = useDeferredValue(query);

  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => initiallyCollapsed(schema.tables));

  // ⌘K, the shortcut the design draws inside the search field. Bound on the
  // document rather than the panel: the participant's hands are in the
  // editor, which is where the shortcut has to work from.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key.toLowerCase() !== "k" || !(event.metaKey || event.ctrlKey)) return;
      event.preventDefault();
      searchRef.current?.focus();
      searchRef.current?.select();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  const matches = useMemo(() => filterTables(schema.tables, deferred), [schema.tables, deferred]);

  return (
    <section aria-label={t.heading} className="flex min-h-0 flex-1 flex-col">
      <header className="flex shrink-0 items-center justify-between gap-2 border-b border-line px-3 py-2 font-mono text-label text-ink-3 uppercase">
        <span>
          {t.heading} · {t.language}
        </span>
        <span>{schema.tables.length}</span>
      </header>

      {/* `relative` for the same reason SidePanel's own panels carry it: the
          search field's `sr-only` label is `position: absolute`, and a static
          scroll box does not clip one. It is near the top here rather than at
          the end of a long list, so it never grew the page the way the
          questions' hidden labels did — but a scroll box that holds
          visually-hidden text has to be the containing block for it either
          way. */}
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
            {/* Decorative: the shortcut is announced by the field's own label,
                and reading "command K" after every placeholder is noise. */}
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
              // A search result opens the tables it matched inside: a hit the
              // participant cannot see is not a hit.
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
  t: Dictionary["participant"]["play"]["schema"];
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
              {/* Shrinkable, not fixed. `shrink-0` here meant the type took
                  whatever it wanted and the *name* absorbed the whole
                  shortfall: at the design's own 212px pane a column called
                  `badge_number` was left 11px of room and rendered as an
                  ellipsis, while `character varying` beside it was printed
                  in full. The name is what a participant has to type into
                  the query; the type is what they can read from the title
                  attribute either way. `shrink-3` weights the giving-way
                  three to one in the name's favour rather than splitting it
                  evenly — measured at the same 212px pane, `badge_number`
                  goes from 79px of it missing to 9px, and `occupation_id`
                  from 59px to 5px — and `truncate` is what keeps a long type
                  from pushing a horizontal scrollbar onto the panel
                  (`timestamp with time zone` overflowed its own row by
                  34px). */}
              <span
                className="ml-auto min-w-0 shrink-3 truncate font-mono text-label text-ink-3 normal-case"
                // The visible text comes first, because it is now the text
                // that can be cut off: a truncated `timestamp with time zone`
                // has to be readable somewhere, and the note that used to be
                // the whole title ("nullable", "foreign key to …") is still
                // there after it. Joined with the same interpunct this panel's
                // own heading uses, so nothing new has to be translated.
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

/** A table narrowed to what the search matched, plus whether the match was inside it. */
type MatchedTable = GameTable & { matchedColumns: boolean };

/**
 * Which tables start collapsed.
 *
 * A game this product is for has a handful of tables, and a participant
 * opening the console wants to see the columns without clicking seven times
 * — so a small schema starts fully open. The API's own bound allows two
 * hundred tables of two hundred columns, and forty thousand rows laid out at
 * once is a frame budget nothing recovers from, so a large one starts closed.
 */
function initiallyCollapsed(tables: readonly GameTable[]): ReadonlySet<string> {
  const rows = tables.reduce((total, table) => total + table.columns.length + 1, 0);
  return rows <= 200 ? new Set<string>() : new Set(tables.map((table) => table.name));
}

/**
 * Tables matching the search, with the ones whose *columns* matched narrowed
 * to those columns.
 *
 * A table matched by its own name keeps all of its columns: the participant
 * asked about the table, not about a column of it.
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

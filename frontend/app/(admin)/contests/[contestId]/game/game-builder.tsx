"use client";

import { useActionState, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { GameDefinition, TableData } from "@/lib/api/game";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { saveGameDefinitionAction, type GameState } from "./actions";
import { GameBuilderTable } from "./game-builder-table";

/** One column of a table, as the editor works on it — `ColumnDefinition`
 * (`lib/api/game.ts`) plus a client-only `key` so a row can be reordered or
 * removed without React losing track of which `<input>` belongs to which
 * column, the same reason `key` exists on `DraftTable` below. */
type DraftColumn = { key: string; name: string; type: string; nullable: boolean };

/**
 * One table, as the editor works on it.
 *
 * `originalName` is the name this table had in the definition the page
 * loaded with, or `null` for a table the organiser has added since — it is
 * what `locked` (below) keys off, and it is deliberately not the same field
 * as `name`: a table already saved may still be renamed while it holds no
 * data, and a rename must not itself unlock a table that has some (an
 * organiser retyping a table's name one letter at a time must not find its
 * own row count reset to "empty" for the length of that edit).
 *
 * `primaryKeyKeys` names columns by their own `key`, not by name, for the
 * same reason: a column may be renamed while the table is unlocked, and a
 * primary key stored as a set of names would silently stop matching the
 * column it meant the moment that rename landed.
 */
type DraftTable = {
  key: string;
  originalName: string | null;
  name: string;
  columns: DraftColumn[];
  primaryKeyKeys: string[];
};

let keyCounter = 0;
/** A client-only identity for a table or column added in this session —
 * `crypto.randomUUID()` would do as well, but this needs no browser API and
 * is exercised the same way under `jsdom`. Never sent to the server: `toWire`
 * strips it back out. */
function nextKey(): string {
  keyCounter += 1;
  return `k${keyCounter}`;
}

/** Turns the saved definition into the editor's own draft shape, minting a
 * client-only `key` for every table and column and translating
 * `primary_key`'s column *names* into the `key`s those columns just got —
 * `DraftTable`'s own doc explains why the editor tracks a primary key by
 * key rather than by name from this point on. */
function initialiseTables(tables: GameDefinition["tables"]): DraftTable[] {
  return tables.map((table) => {
    const columns = table.columns.map((c) => ({ key: nextKey(), name: c.name, type: c.type, nullable: c.nullable }));
    const primaryKeyKeys = columns.filter((c) => table.primaryKey.includes(c.name)).map((c) => c.key);
    return { key: nextKey(), originalName: table.name, name: table.name, columns, primaryKeyKeys };
  });
}

function toWireDefinition(tables: DraftTable[]) {
  return {
    tables: tables.map((table) => ({
      name: table.name,
      columns: table.columns.map((c) => ({ name: c.name, type: c.type, nullable: c.nullable })),
      primary_key: table.columns.filter((c) => table.primaryKeyKeys.includes(c.key)).map((c) => c.name),
    })),
  };
}

/**
 * The third way to build a contest's game: tables and columns described
 * directly, alongside the SQL editor and the finished-dump upload above it
 * on this same page. `page.tsx`'s own doc explains why all three stay on
 * one screen rather than a tab each — an organiser choosing between them is
 * choosing between three ways to produce the very thing `GameEditor`'s own
 * status tag already reports on.
 *
 * # Why a table with data locks its own structure
 *
 * `SetDefinition` refuses a save that would change the name, columns or
 * primary key of a table that already holds data
 * (`provisioning.ErrDefinitionTableLocked`, `game_definition_table_locked`
 * on the wire) — a rename or a column's removal disagrees with the file's
 * own header at the next build; a type change is worse, because the header
 * only names columns, never their types, so a value that still happens to
 * parse under the new one loads silently. That refusal is real and this
 * screen cannot be bypassed around it (talking to the API directly still
 * gets the 409), but waiting for a request to come back to say "no" is a
 * worse experience than not offering the edit at all, so this screen still
 * disables it client-side first: a table whose row count (`rowCounts`,
 * lifted from `page.tsx`'s own best-effort read and kept current from every
 * write `GameBuilderTable` makes) is above zero has its name, columns and
 * primary key disabled, with `lockedTable`'s own sentence saying why, and
 * only removing that data first lifts the lock. Adding an all-new table, or
 * editing one that still has none, is never affected.
 *
 * `rowCounts` is deliberately `Lines`, not the deletion-adjusted count: the
 * server's own check above locks on a table's rows existing at all, ever
 * — a tombstoned row still leaves its file's old header behind — so a
 * delete that emptied every active row must not, on its own, read as
 * "unlocked" here. `activeRowCounts` (below) is the other number,
 * `ActiveRows()`, kept apart for exactly that reason: it is what
 * `lockedTable`'s own sentence shows, and it is the one a delete actually
 * moves.
 */
export function GameBuilder({
  contestId,
  definition,
  rowCounts: initialRowCounts,
  currentTableUploads = {},
  editable,
  dict,
}: {
  contestId: string;
  /** The definition as `page.tsx` read it, and the table names the data
   * section below may show a tab for — `GameBuilderTable` reads
   * `SetDefinition`'s own current, saved structure, never this component's
   * unsaved draft. */
  definition: GameDefinition;
  /** Each table's row count as the page loaded, keyed by its *current*
   * name. Lifted into local state below because every write a table's own
   * data panel makes (`GameBuilderTable`'s `onRowCountChange`) has to be
   * reflected here immediately — the lock this component enforces would
   * otherwise only ever see the count the page happened to load with. */
  rowCounts: Record<string, number>;
  /** Each table's own chunked CSV upload the page found still receiving,
   * keyed by table name, or null — `page.tsx`'s own `tableCurrentUpload`,
   * passed straight through to `GameBuilderTable` so a reload can resume
   * an unfinished table upload the same way `GameUpload` already resumes an
   * unfinished dump. Read once, unlike `rowCounts`: an upload's own state
   * lives entirely inside `GameBuilderTable` afterward and never needs this
   * component to keep it current. */
  currentTableUploads?: Record<string, TableData | null>;
  editable: boolean;
  dict: Dictionary;
}) {
  const tb = dict.workspace.game.builder;
  const errors = dict.errors;
  const limits = definition.builderLimits;

  const [tables, setTables] = useState<DraftTable[]>(() => initialiseTables(definition.tables));
  // `rowCounts` is `TableData.Lines` per table — never reduced by a delete
  // (a tombstoned row leaves the file's own header exactly as it was), and
  // the one `locked` below reads, since that is what the server's own
  // structure lock actually checks (`checkTableDataCompatibility`,
  // `tabledata.go`). `activeRowCounts` is `TableData.ActiveRows()`, the
  // deletion-adjusted count `TableCard` shows next to a locked table's own
  // name — seeded from the same initial read (the best guess available
  // before any write has reported the true split) and kept current
  // separately from then on, exactly the two `GameBuilderTable` callbacks
  // below report separately. One shared variable for both used to mean a
  // delete's own report (lower) and the very next window fetch's report
  // (unchanged) fought over what a single number meant.
  const [rowCounts, setRowCounts] = useState<Record<string, number>>(initialRowCounts);
  const [activeRowCounts, setActiveRowCounts] = useState<Record<string, number>>(initialRowCounts);
  const [activeTable, setActiveTable] = useState(definition.tables[0]?.name ?? "");

  const [state, save, saving] = useActionState<GameState, FormData>(saveGameDefinitionAction, {});

  function locked(table: DraftTable): boolean {
    return table.originalName !== null && (rowCounts[table.originalName] ?? 0) > 0;
  }

  function updateTable(key: string, updater: (table: DraftTable) => DraftTable) {
    setTables((prev) => prev.map((t) => (t.key === key ? updater(t) : t)));
  }

  function addTable() {
    setTables((prev) => [...prev, { key: nextKey(), originalName: null, name: "", columns: [], primaryKeyKeys: [] }]);
  }

  function removeTable(key: string, name: string) {
    if (!window.confirm(tb.removeTableConfirm.replace("{name}", name))) return;
    setTables((prev) => prev.filter((t) => t.key !== key));
  }

  function addColumn(tableKey: string) {
    updateTable(tableKey, (t) => ({
      ...t,
      columns: [...t.columns, { key: nextKey(), name: "", type: limits.columnTypes[0] ?? "text", nullable: false }],
    }));
  }

  function removeColumn(tableKey: string, columnKey: string) {
    updateTable(tableKey, (t) => ({
      ...t,
      columns: t.columns.filter((c) => c.key !== columnKey),
      primaryKeyKeys: t.primaryKeyKeys.filter((k) => k !== columnKey),
    }));
  }

  function updateColumn(tableKey: string, columnKey: string, updater: (column: DraftColumn) => DraftColumn) {
    updateTable(tableKey, (t) => ({
      ...t,
      columns: t.columns.map((c) => (c.key === columnKey ? updater(c) : c)),
    }));
  }

  function togglePrimaryKey(tableKey: string, columnKey: string, checked: boolean) {
    updateTable(tableKey, (t) => ({
      ...t,
      primaryKeyKeys: checked ? [...t.primaryKeyKeys, columnKey] : t.primaryKeyKeys.filter((k) => k !== columnKey),
    }));
  }

  const wire = toWireDefinition(tables);
  const definitionBytes = new TextEncoder().encode(JSON.stringify(wire)).length;
  const oversized = definitionBytes > limits.maxDefinitionBytes;
  const canSave = editable && !saving && tables.length > 0 && !oversized;

  return (
    <section className="flex flex-col gap-4 border-t border-line pt-8">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-h3 text-ink">{tb.heading}</h3>
        <p className="max-w-body text-small text-ink-2">{tb.lede}</p>
      </div>

      {!editable ? <p className="text-small text-ink-2">{dict.workspace.game.frozen}</p> : null}

      <form action={save} className="flex flex-col gap-5">
        <input type="hidden" name="contestId" value={contestId} />
        <input type="hidden" name="definition" value={JSON.stringify(wire)} readOnly />

        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h4 className="text-control text-ink">{tb.structureHeading}</h4>
            <span className="text-label text-ink-3">
              {tb.limitTables.replace("{n}", String(tables.length)).replace("{max}", String(limits.maxTables))}
            </span>
          </div>

          {tables.length === 0 ? <p className="text-small text-ink-2">{tb.emptyTables}</p> : null}

          {tables.map((table) => (
            <TableCard
              key={table.key}
              table={table}
              editable={editable}
              locked={locked(table)}
              rowCount={table.originalName ? activeRowCounts[table.originalName] ?? 0 : 0}
              limits={limits}
              dict={dict}
              onNameChange={(name) => updateTable(table.key, (t) => ({ ...t, name }))}
              onRemove={() => removeTable(table.key, table.name)}
              onAddColumn={() => addColumn(table.key)}
              onRemoveColumn={(columnKey) => removeColumn(table.key, columnKey)}
              onColumnNameChange={(columnKey, name) =>
                updateColumn(table.key, columnKey, (c) => ({ ...c, name }))
              }
              onColumnTypeChange={(columnKey, type) =>
                updateColumn(table.key, columnKey, (c) => ({ ...c, type }))
              }
              onColumnNullableChange={(columnKey, nullable) =>
                updateColumn(table.key, columnKey, (c) => ({ ...c, nullable }))
              }
              onTogglePrimaryKey={(columnKey, checked) => togglePrimaryKey(table.key, columnKey, checked)}
            />
          ))}

          <div>
            <button
              type="button"
              disabled={!editable || tables.length >= limits.maxTables}
              onClick={addTable}
              className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
            >
              {tb.addTable}
            </button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <button type="submit" disabled={!canSave} className={cn(buttonVariants({ variant: "primary" }))}>
            {saving ? tb.saving : tb.save}
          </button>
          <span className={cn("font-mono text-label", oversized ? "text-bad" : "text-ink-3")}>
            {tb.sizeHint.replace("{n}", String(definitionBytes)).replace("{max}", String(limits.maxDefinitionBytes))}
          </span>
          {oversized ? <span className="text-small text-bad">{tb.tooLarge}</span> : null}
          <span role="status" aria-live="polite" className="text-small text-ink-2">
            {state.code ? ((errors as Record<string, string>)[state.code] ?? errors.fallback) : state.saved ? tb.saved : ""}
          </span>
        </div>
        {/* `Definition.Validate`'s own refusals name a table or column
            (`ErrDefinitionInvalidName` and friends — `actions.ts`'s own
            `GameState.detail` doc explains why that text is worth carrying
            up), shown beside the dictionary's own sentence above rather
            than in place of it. */}
        {state.code && state.detail ? (
          <div className="flex flex-col gap-1">
            <p className="text-label text-ink-3">{tb.detailLabel}</p>
            <pre className="max-w-body overflow-x-auto font-mono text-data text-bad">{state.detail}</pre>
          </div>
        ) : null}
      </form>

      <div className="flex flex-col gap-4 border-t border-line pt-6">
        <h4 className="text-control text-ink">{tb.data.heading}</h4>

        {!limits.enabled ? (
          <p className="max-w-body text-small text-ink-2">{tb.data.dataDisabled}</p>
        ) : definition.tables.length === 0 ? (
          <p className="text-small text-ink-2">{tb.data.noTables}</p>
        ) : (
          <Tabs value={activeTable} onValueChange={setActiveTable}>
            <TabsList>
              {definition.tables.map((table) => (
                <TabsTrigger key={table.name} value={table.name}>
                  {table.name}
                </TabsTrigger>
              ))}
            </TabsList>
            {definition.tables.map((table) => (
              <TabsContent key={table.name} value={table.name} fill={false} className="pt-4">
                <GameBuilderTable
                  contestId={contestId}
                  table={table}
                  active={activeTable === table.name}
                  limits={limits}
                  editable={editable}
                  onRowCountChange={(count) => setRowCounts((prev) => ({ ...prev, [table.name]: count }))}
                  activeRowCount={activeRowCounts[table.name] ?? 0}
                  onActiveRowCountChange={(count) =>
                    setActiveRowCounts((prev) => ({ ...prev, [table.name]: count }))
                  }
                  initialTableData={currentTableUploads[table.name] ?? null}
                  dict={dict}
                />
              </TabsContent>
            ))}
          </Tabs>
        )}
      </div>
    </section>
  );
}

/** One table's own structure: its name, its columns, and which of them make
 * up its primary key. A pure presentational block — every edit it reports
 * goes back up to `GameBuilder`'s own state through the callbacks it is
 * given, so this component holds nothing itself that could drift from the
 * `DraftTable` it was handed. */
function TableCard({
  table,
  editable,
  locked,
  rowCount,
  limits,
  dict,
  onNameChange,
  onRemove,
  onAddColumn,
  onRemoveColumn,
  onColumnNameChange,
  onColumnTypeChange,
  onColumnNullableChange,
  onTogglePrimaryKey,
}: {
  table: DraftTable;
  editable: boolean;
  locked: boolean;
  /** The active (deletion-adjusted) row count shown in `lockedTable`'s own
   * sentence — `GameBuilder`'s own `activeRowCounts`, never the `rowCounts`
   * that decides `locked` itself: the two answer different questions (this
   * component's own doc on why). */
  rowCount: number;
  limits: GameDefinition["builderLimits"];
  dict: Dictionary;
  onNameChange: (name: string) => void;
  onRemove: () => void;
  onAddColumn: () => void;
  onRemoveColumn: (columnKey: string) => void;
  onColumnNameChange: (columnKey: string, name: string) => void;
  onColumnTypeChange: (columnKey: string, type: string) => void;
  onColumnNullableChange: (columnKey: string, nullable: boolean) => void;
  onTogglePrimaryKey: (columnKey: string, checked: boolean) => void;
}) {
  const tb = dict.workspace.game.builder;
  const disabled = !editable || locked;

  return (
    <div className="flex flex-col gap-3 border border-line-2 p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <label className="flex items-center gap-2">
          <span className="text-label text-ink-3">{tb.tableNameLabel}</span>
          <Input
            value={table.name}
            disabled={disabled}
            onChange={(event) => onNameChange(event.target.value)}
            className="w-52"
          />
        </label>
        <div className="flex items-center gap-3">
          <span className="text-label text-ink-3">
            {tb.limitColumns.replace("{n}", String(table.columns.length)).replace("{max}", String(limits.maxTableColumns))}
          </span>
          <button
            type="button"
            disabled={!editable || locked}
            onClick={onRemove}
            className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
          >
            {tb.removeTable}
          </button>
        </div>
      </div>

      {locked ? (
        <p className="max-w-body text-small text-warn">{tb.lockedTable.replace("{n}", String(rowCount))}</p>
      ) : null}

      <div className="overflow-x-auto">
        <div className="flex min-w-max flex-col gap-2">
          <div className="flex gap-3 text-label text-ink-3 uppercase">
            <span className="w-44">{tb.columnNameLabel}</span>
            <span className="w-36">{tb.columnTypeLabel}</span>
            <span className="w-32">{tb.nullableLabel}</span>
            <span className="w-28">{tb.primaryKeyLabel}</span>
            <span className="w-16" />
          </div>
          {table.columns.map((column) => (
            <div key={column.key} className="flex items-center gap-3">
              <Input
                value={column.name}
                disabled={disabled}
                onChange={(event) => onColumnNameChange(column.key, event.target.value)}
                className="w-44"
              />
              <select
                value={column.type}
                disabled={disabled}
                onChange={(event) => onColumnTypeChange(column.key, event.target.value)}
                className={cn(
                  "h-(--control-h) w-36 rounded-none border border-edge bg-transparent px-3",
                  "text-control text-ink transition-colors duration-(--t-input) ease-standard",
                  "hover:border-ink-2 focus-visible:border-ink",
                  "disabled:cursor-not-allowed disabled:bg-sunk disabled:text-ink-3",
                )}
              >
                {limits.columnTypes.map((type) => (
                  <option key={type} value={type}>
                    {(tb.columnTypes as Record<string, string>)[type] ?? type}
                  </option>
                ))}
              </select>
              <span className="flex w-32 items-center">
                <Checkbox
                  checked={column.nullable}
                  disabled={disabled}
                  onCheckedChange={(checked) => onColumnNullableChange(column.key, checked === true)}
                  aria-label={tb.nullableLabel}
                />
              </span>
              <span className="flex w-28 items-center">
                <Checkbox
                  checked={table.primaryKeyKeys.includes(column.key)}
                  disabled={disabled}
                  onCheckedChange={(checked) => onTogglePrimaryKey(column.key, checked === true)}
                  aria-label={tb.primaryKeyLabel}
                />
              </span>
              <span className="w-16">
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => onRemoveColumn(column.key)}
                  className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
                >
                  {tb.removeColumn}
                </button>
              </span>
            </div>
          ))}
        </div>
      </div>

      <div>
        <button
          type="button"
          disabled={disabled || table.columns.length >= limits.maxTableColumns}
          onClick={onAddColumn}
          className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
        >
          {tb.addColumn}
        </button>
      </div>
    </div>
  );
}

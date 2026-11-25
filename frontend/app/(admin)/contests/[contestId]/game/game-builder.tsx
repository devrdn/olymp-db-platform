"use client";

import { useState } from "react";

import { ActionButton, ActionForm, ActionOutcome, useDraftEdited } from "@/components/product/action-form";
import { buttonVariants } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { type Question, useConfirm } from "@/components/ui/use-confirm";
import type { Contest } from "@/lib/api/contests";
import { atVersion } from "@/lib/api/contests-terms";
import type { GameDefinition, TableData } from "@/lib/api/game";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill, isolate } from "@/lib/i18n/fill";
import { labelFor } from "@/lib/i18n/label";
import { cn } from "@/lib/utils";

import { ReloadWhenChanged } from "../reload-when-changed";
import { saveGameDefinitionAction } from "./actions";
import { GameBuilderTable } from "./game-builder-table";

/**
 * `ColumnDefinition` plus a client-only `key`, so reordering or removing keeps
 * each `<input>` with its column.
 */
type DraftColumn = { key: string; name: string; type: string; nullable: boolean };

/**
 * One table in the editor. `originalName` is the saved name (`null` for a new
 * table) and is what `locked` keys off, so renaming one letter at a time never
 * unlocks a table with data. `primaryKeyKeys` uses column keys, not names, so a
 * rename does not break the primary key.
 */
type DraftTable = {
  key: string;
  originalName: string | null;
  name: string;
  columns: DraftColumn[];
  primaryKeyKeys: string[];
};

let keyCounter = 0;
/** Client-only identity for a table or column; `toWire` strips it. */
function nextKey(): string {
  keyCounter += 1;
  return `k${keyCounter}`;
}

/**
 * Builds the draft from the saved definition, minting keys and mapping
 * `primary_key` names to them.
 */
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
 * Builds the game from tables and columns described directly.
 *
 * The server refuses changes to the name, columns or primary key of a table
 * holding data (`game_definition_table_locked`): a rename or removed column
 * breaks the file header at the next build, and a type change can load silently
 * since the header has no types. This screen disables those edits first rather
 * than waiting for the 409.
 *
 * The lock reads `rowCounts` (`Lines`), not the deletion-adjusted count: a
 * tombstone leaves the old header behind, so deleting every row must not
 * unlock. `activeRowCounts` is what the sentence shows.
 */
export function GameBuilder({
  contest,
  definition,
  rowCounts: initialRowCounts,
  currentTableUploads = {},
  editable,
  dict,
}: {
  contest: Pick<Contest, "id" | "version">;
  /** The saved definition; `GameBuilderTable` reads this, never the unsaved draft. */
  definition: GameDefinition;
  /**
   * Each table's `Lines` at page load, by current name. Kept in local state
   * because every data write must update the lock immediately.
   */
  rowCounts: Record<string, number>;
  /**
   * Each table's upload still receiving at page load, by name, passed to
   * `GameBuilderTable` for resuming. Read once.
   */
  currentTableUploads?: Record<string, TableData | null>;
  editable: boolean;
  dict: Dictionary;
}) {
  const tb = dict.workspace.game.builder;
  const [ask, confirmation] = useConfirm();
  const errors = dict.errors;
  const limits = definition.builderLimits;

  // `rowCounts` (`Lines`) drives the lock and never drops on a delete;
  // `activeRowCounts` is what `TableCard` shows. Both start from the same read
  // and are updated separately, since one shared number let a delete and a
  // later window fetch overwrite each other.
  const [rowCounts, setRowCounts] = useState<Record<string, number>>(initialRowCounts);
  const [activeRowCounts, setActiveRowCounts] = useState<Record<string, number>>(initialRowCounts);
  const [activeTable, setActiveTable] = useState(definition.tables[0]?.name ?? "");

  function locked(table: DraftTable): boolean {
    return table.originalName !== null && (rowCounts[table.originalName] ?? 0) > 0;
  }

  return (
    <section className="flex flex-col gap-4 border-t border-line pt-8">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-h3 text-ink">{tb.heading}</h3>
        <p className="max-w-body text-small text-ink-2">{tb.lede}</p>
      </div>

      {!editable ? <p className="text-small text-ink-2">{dict.workspace.game.frozen}</p> : null}

      <ActionForm
        action={saveGameDefinitionAction}
        {...atVersion(contest, JSON.stringify(definition.tables))}
        errors={errors}
        className="flex flex-col gap-5"
      >
        {(state) => (
          <StructureEditor
            saved={definition.tables}
            limits={limits}
            editable={editable}
            locked={locked}
            activeRowCounts={activeRowCounts}
            ask={ask}
            detail={state.code ? state.detail : undefined}
            dict={dict}
          />
        )}
      </ActionForm>

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
                  contestId={contest.id}
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
      {confirmation}
    </section>
  );
}

/**
 * The structure being edited, inside the form so a reload starts it over from
 * the saved definition. Its draft is held in state, so every change also marks
 * the form edited (`useDraftEdited`): a button that adds or removes a table or
 * column fires no input event.
 */
function StructureEditor({
  saved,
  limits,
  editable,
  locked,
  activeRowCounts,
  ask,
  detail,
  dict,
}: {
  saved: GameDefinition["tables"];
  limits: GameDefinition["builderLimits"];
  editable: boolean;
  locked: (table: DraftTable) => boolean;
  activeRowCounts: Record<string, number>;
  ask: (question: Question) => Promise<boolean>;
  /** The API's message beside a refusal that names the table or column at fault. */
  detail?: string;
  dict: Dictionary;
}) {
  const tb = dict.workspace.game.builder;
  const edited = useDraftEdited();
  const [tables, setDraft] = useState<DraftTable[]>(() => initialiseTables(saved));
  const setTables = (update: (prev: DraftTable[]) => DraftTable[]) => {
    edited();
    setDraft(update);
  };

  function updateTable(key: string, updater: (table: DraftTable) => DraftTable) {
    setTables((prev) => prev.map((t) => (t.key === key ? updater(t) : t)));
  }

  function addTable() {
    setTables((prev) => [...prev, { key: nextKey(), originalName: null, name: "", columns: [], primaryKeyKeys: [] }]);
  }

  async function removeTable(key: string, name: string) {
    const question = {
      title: fill(tb.removeTableConfirm, { name: isolate(name) }),
      answer: tb.removeTable,
      cancel: dict.chrome.cancel,
      danger: true,
    };
    if (!(await ask(question).catch(() => false))) return;
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
  const canSave = editable && tables.length > 0 && !oversized;

  return (
    <>
      <input type="hidden" name="definition" value={JSON.stringify(wire)} />
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h4 className="text-control text-ink">{tb.structureHeading}</h4>
          <span className="text-label text-ink-3">
            {fill(tb.limitTables, { n: tables.length, max: limits.maxTables })}
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
            onRemove={() => void removeTable(table.key, table.name)}
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
        <ActionButton disabled={!canSave} pendingLabel={tb.saving}>
          {tb.save}
        </ActionButton>
        <span className={cn("font-mono text-label", oversized ? "text-bad" : "text-ink-3")}>
          {fill(tb.sizeHint, { n: definitionBytes, max: limits.maxDefinitionBytes })}
        </span>
        {oversized ? <span className="text-small text-bad">{tb.tooLarge}</span> : null}
        <ActionOutcome saved={tb.saved} />
      </div>
      <ReloadWhenChanged label={dict.workspace.reload} />
      {/* Validation refusals name the table or column; shown beside the dictionary sentence. */}
      {detail ? (
        <div className="flex flex-col gap-1">
          <p className="text-label text-ink-3">{tb.detailLabel}</p>
          <pre className="max-w-body overflow-x-auto font-mono text-data text-bad">{detail}</pre>
        </div>
      ) : null}
    </>
  );
}

/**
 * One table's name, columns and primary key. Presentational: every edit goes
 * back up through callbacks.
 */
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
  /** Active row count for the locked sentence, not the count that decides `locked`. */
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
            {fill(tb.limitColumns, { n: table.columns.length, max: limits.maxTableColumns })}
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
        <p className="max-w-body text-small text-warn">{fill(tb.lockedTable, { n: rowCount })}</p>
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
                    {labelFor(tb.columnTypes, type) ?? type}
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

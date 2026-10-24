"use client";

import { memo, useCallback, useId, useLayoutEffect, useMemo, useRef, useState } from "react";

import Link from "next/link";

import { Checkbox } from "@/components/ui/checkbox";
import { Tooltip } from "@/components/ui/tooltip";
import { MONITOR_FLAGS, type MonitorFlags, type RosterRow } from "@/lib/api/monitor";
import { REGISTRATION_STATUSES } from "@/lib/api/people";
import { readableDuration } from "@/lib/format/bytes";
import { formatMoment, formatTime } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  COUNTER_KEYS,
  filterRows,
  sortRows,
  widestCells,
  type CounterKey,
  type RosterFilter,
  type SortKey,
  type SortOrder,
  type WidestCells,
} from "./roster";
import { pieces, remToPx, rowWindow, UNMEASURED_ROWS, type RowRange } from "./row-window";

type MonitorDict = Dictionary["workspace"]["monitor"];

/** Column order, and each column's first sort direction. */
const COLUMNS: { key: SortKey; numeric: boolean }[] = [
  { key: "name", numeric: false },
  { key: "status", numeric: false },
  { key: "flags", numeric: true },
  ...COUNTER_KEYS.map((key) => ({ key, numeric: true })),
  { key: "lastActivity", numeric: true },
];

const CONTROL = "h-(--control-h) border border-edge bg-bg px-2.5 text-control text-ink";

/**
 * Row height in rem, a little above what the name and login lines (each held
 * to its line height), the padding and the rule need, so every row is the same
 * height and the window is arithmetic.
 */
export const ROSTER_ROW_REM = 3.375;

/**
 * Every participant with counters and flags (SPEC.md §5.1). Scrolls both ways
 * inside its own box with the name column and heading held. Only the rows in
 * view are rendered, at a fixed height, between spacer rows that keep the
 * scroll height; sorting and filtering still work on the whole list. The row
 * holding focus is rendered wherever it is, so scrolling or a poll's re-sort
 * never drops the keyboard to the page. Rows are memoised and arrive merged
 * (`mergeRoster`), so a poll that changed one participant renders at most one
 * row.
 */
export function ParticipantsTable({
  contestId,
  rows,
  truncated,
  fresh,
  dict,
  locale,
}: {
  contestId: string;
  rows: RosterRow[];
  truncated: boolean;
  /** Participants with a fresh feed item; their rows are lit. */
  fresh: ReadonlySet<string>;
  dict: Pick<Dictionary, "workspace">;
  locale: string;
}) {
  const t = dict.workspace.monitor;
  const statuses = dict.workspace.people.registration as Record<string, string>;
  const [order, setOrder] = useState<SortOrder>({ key: "name", dir: "asc" });
  const [filter, setFilter] = useState<RosterFilter>({ flaggedOnly: false, status: "", search: "" });
  const ids = useId();

  const shown = useMemo(() => sortRows(filterRows(rows, filter), order), [rows, filter, order]);
  const widest = useMemo(() => widestCells(shown), [shown]);
  const total = shown.length;

  const scrollRef = useRef<HTMLDivElement>(null);
  const headRef = useRef<HTMLTableSectionElement>(null);
  const rowHeightRef = useRef(ROSTER_ROW_REM * 16);
  const [range, setRange] = useState<RowRange>({ start: 0, end: UNMEASURED_ROWS });
  // The participant whose row holds focus; that row is rendered even outside
  // the window. A re-sort that moves its node blurs it, and React's commit
  // focuses it again, since the node is still in the document.
  const [held, setHeld] = useState<string | null>(null);

  const measure = useCallback(() => {
    const element = scrollRef.current;
    if (!element) return;
    const { start, end } = rowWindow(element, rowHeightRef.current, headRef.current?.offsetHeight ?? 0);
    setRange((current) => (current.start === start && current.end === end ? current : { start, end }));
  }, []);

  // A shorter list may have pulled the offset back; the box may have just
  // appeared after "no match".
  useLayoutEffect(() => {
    rowHeightRef.current = remToPx(ROSTER_ROW_REM);
    measure();
  }, [total, measure]);

  // A taller box needs more rows. The box exists only while a row is shown.
  const boxed = total > 0;
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [measure, boxed]);

  const heldIndex = held === null ? -1 : shown.findIndex((row) => row.registrationId === held);
  const bodyRows = pieces(range, total, heldIndex).flatMap((piece) =>
    piece.kind === "gap"
      ? [<Spacer key={piece.key} rows={piece.rows} />]
      : shown.slice(piece.from, piece.to).map((row, i) => (
          <ParticipantRow
            key={row.registrationId}
            row={row}
            index={piece.from + i}
            fresh={fresh.has(row.registrationId)}
            contestId={contestId}
            t={t}
            statuses={statuses}
            locale={locale}
          />
        )),
  );

  const onBodyFocus = (event: React.FocusEvent) => {
    const id = (event.target as HTMLElement).closest<HTMLElement>("tr[data-row]")?.dataset.row;
    if (id) setHeld(id);
  };
  const onBodyBlur = (event: React.FocusEvent<HTMLTableSectionElement>) => {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setHeld(null);
  };

  const sortBy = (key: SortKey, numeric: boolean) =>
    setOrder((current) =>
      current.key === key
        ? { key, dir: current.dir === "asc" ? "desc" : "asc" }
        : { key, dir: numeric ? "desc" : "asc" },
    );

  const label = (key: SortKey) => t.table.columns[key];
  const filtered = filter.flaggedOnly || filter.status !== "" || filter.search.trim() !== "";

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
        <div className="flex min-w-0 grow basis-48 flex-col gap-1.5">
          <label htmlFor={`${ids}-search`} className="font-mono text-label text-ink-3 uppercase">
            {t.table.filters.search}
          </label>
          <input
            id={`${ids}-search`}
            type="search"
            value={filter.search}
            placeholder={t.table.filters.searchPlaceholder}
            onChange={(event) => setFilter((current) => ({ ...current, search: event.target.value }))}
            className={cn(CONTROL, "w-full min-w-0 placeholder:text-ink-3")}
          />
        </div>
        <div className="flex min-w-0 grow basis-40 flex-col gap-1.5">
          <label htmlFor={`${ids}-status`} className="font-mono text-label text-ink-3 uppercase">
            {t.table.filters.status}
          </label>
          <select
            id={`${ids}-status`}
            value={filter.status}
            onChange={(event) => setFilter((current) => ({ ...current, status: event.target.value }))}
            className={cn(CONTROL, "w-full")}
          >
            <option value="">{t.table.filters.anyStatus}</option>
            {REGISTRATION_STATUSES.map((status) => (
              <option key={status} value={status}>
                {statuses[status] ?? status}
              </option>
            ))}
          </select>
        </div>
        <label className="flex h-(--control-h) items-center gap-2 text-control text-ink">
          <Checkbox
            checked={filter.flaggedOnly}
            onCheckedChange={(checked) => setFilter((current) => ({ ...current, flaggedOnly: checked === true }))}
          />
          {t.table.filters.flaggedOnly}
        </label>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="font-mono text-data text-ink-3">
          {t.table.count.replace("{shown}", String(shown.length)).replace("{total}", String(rows.length))}
        </p>
        {/* Above the table: inside its scroll box the explanation would be clipped. */}
        <span className="inline-flex items-center gap-1 text-small text-ink-3">
          <span aria-hidden>{t.flags.help}</span>
          <FlagsHelp t={t} />
        </span>
        {truncated ? (
          <p className="text-small text-warn">{t.table.truncated.replace("{n}", String(rows.length))}</p>
        ) : null}
      </div>

      {rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.table.empty}</p>
      ) : shown.length === 0 && filtered ? (
        <p className="text-body text-ink-2">{t.table.noMatch}</p>
      ) : (
        <div
          ref={scrollRef}
          data-testid="roster-scroller"
          onScroll={measure}
          className="max-h-[42rem] overflow-auto border-y border-line max-narrow:max-h-[70vh]"
        >
          {/* Separate borders: a collapsed border belongs to the table and would
             scroll away from the held cells. The row count and indexes tell
             assistive technology about the rows that are not rendered. */}
          <table
            aria-rowcount={total + 1}
            className="w-max min-w-full border-separate text-left"
            style={{ borderSpacing: 0 }}
          >
            <caption className="sr-only">{t.table.heading}</caption>
            <thead ref={headRef}>
              <tr aria-rowindex={1}>
                {COLUMNS.map(({ key, numeric }, index) => {
                  const sorted = order.key === key;
                  return (
                    <th
                      key={key}
                      scope="col"
                      aria-sort={sorted ? (order.dir === "asc" ? "ascending" : "descending") : undefined}
                      className={cn(
                        "sticky top-0 z-1 border-b border-line-2 bg-bg px-2 py-2 align-bottom font-mono text-label font-medium text-ink-3 uppercase",
                        numeric && key !== "flags" && "text-right",
                        // The corner cell, held on both axes above both.
                        index === 0 && "left-0 z-2",
                      )}
                    >
                      <span className={cn("inline-flex items-center", numeric && key !== "flags" && "justify-end")}>
                        <button
                          type="button"
                          onClick={() => sortBy(key, numeric)}
                          aria-label={label(key)}
                          title={t.table.sortBy.replace("{column}", label(key))}
                          className={cn(
                            "max-w-24 text-left uppercase transition-colors duration-(--t-input) ease-standard hover:text-ink",
                            sorted && "text-ink",
                          )}
                        >
                          {label(key)}
                          {sorted ? <span aria-hidden>{order.dir === "asc" ? " ↑" : " ↓"}</span> : null}
                        </button>
                      </span>
                    </th>
                  );
                })}
              </tr>
            </thead>
            <tbody onFocus={onBodyFocus} onBlur={onBodyBlur}>
              <ColumnSizer widest={widest} t={t} statuses={statuses} locale={locale} />
              {bodyRows}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function FlagsHelp({ t }: { t: MonitorDict }) {
  return (
    <Tooltip label={t.flags.help}>
      <span className="block">{t.flags.notProof}</span>
      <span className="mt-2 flex flex-col gap-1.5">
        {MONITOR_FLAGS.map((flag) => (
          <span key={flag} className="block">
            {t.flags[flag].explain}
          </span>
        ))}
      </span>
    </Tooltip>
  );
}

/** Raised flags as badges, for the table and a participant's page. */
export function FlagBadges({ flags, t }: { flags: MonitorFlags; t: MonitorDict }) {
  return (
    <span className="flex flex-wrap gap-1">
      {MONITOR_FLAGS.filter((flag) => flags[flag]).map((flag) => (
        <span
          key={flag}
          title={t.flags[flag].explain}
          className="rounded-full bg-warn-wash px-1.5 font-mono text-label whitespace-nowrap text-warn uppercase"
        >
          <span aria-hidden>{t.flags[flag].label}</span>
          {/* The title serves pointers; this serves everyone else. */}
          <span className="sr-only">{t.flags[flag].explain}</span>
        </span>
      ))}
    </span>
  );
}

const CELL_X = "px-2";
const CELL = cn(CELL_X, "border-b border-line py-1.5");
const NUMBER = "text-right font-mono text-data tabular-nums";
const NAME_WIDTH = "max-w-56 min-w-40";
// One line of the element's own line height (`lh`), so a fallback glyph with a
// taller ascent cannot push a row past `ROSTER_ROW_REM`.
const ONE_LINE = "h-[1lh]";

/** Stands in for rows outside the window, so the scroll height stays that of the whole list. */
function Spacer({ rows }: { rows: number }) {
  return (
    <tr aria-hidden data-spacer="" style={{ height: `${rows * ROSTER_ROW_REM}rem` }}>
      <td colSpan={COLUMNS.length} className="p-0" />
    </tr>
  );
}

/**
 * A row of no height holding each column's widest content from the whole list
 * (`widestCells`), so the columns keep their width while rows scroll in and
 * out. A cell's content adds to its column's width even when it adds no
 * height.
 */
const ColumnSizer = memo(
  function ColumnSizer({
    widest,
    t,
    statuses,
    locale,
  }: {
    widest: WidestCells;
    t: MonitorDict;
    statuses: Record<string, string>;
    locale: string;
  }) {
    const hold = "h-0 overflow-hidden";
    return (
      <tr aria-hidden data-sizer="">
        <td className={cn(CELL_X, NAME_WIDTH)}>
          <div className={hold}>
            <span className="block truncate text-control">{widest.name}</span>
            <span className="block truncate font-mono text-label">{widest.login}</span>
          </div>
        </td>
        <td className={cn(CELL_X, "text-small whitespace-nowrap")}>
          <div className={hold}>
            {widest.statuses.map((status) => (
              <span key={status} className="block">
                {statuses[status] ?? status}
              </span>
            ))}
          </div>
        </td>
        <td className={cn(CELL_X, "min-w-28")}>
          <div className={hold}>
            {widest.flagSets.map((flags, i) => (
              <FlagBadges key={i} flags={flags} t={t} />
            ))}
          </div>
        </td>
        {COUNTER_KEYS.map((key) => (
          <td key={key} className={cn(CELL_X, NUMBER)}>
            <div className={hold}>
              {key === "awayMs" ? readableDuration(widest.counters.awayMs / 1000) : widest.counters[key]}
            </div>
          </td>
        ))}
        <td className={cn(CELL_X, NUMBER, "whitespace-nowrap")}>
          <div className={hold}>
            {widest.activity ? <span className="block">{formatTime(widest.activity, { locale })}</span> : null}
            {widest.inactive ? <span className="block">{t.table.never}</span> : null}
          </div>
        </td>
      </tr>
    );
  },
  // Recomputed whenever any row changes, but its content rarely does.
  (previous, next) =>
    previous.t === next.t &&
    previous.statuses === next.statuses &&
    previous.locale === next.locale &&
    JSON.stringify(previous.widest) === JSON.stringify(next.widest),
);

/** One participant, memoised on a row object that survives an unchanged poll. */
const ParticipantRow = memo(function ParticipantRow({
  row,
  index,
  fresh,
  contestId,
  t,
  statuses,
  locale,
}: {
  row: RosterRow;
  /** Position in the sorted, filtered list. */
  index: number;
  fresh: boolean;
  contestId: string;
  t: MonitorDict;
  statuses: Record<string, string>;
  locale: string;
}) {
  const count = (key: CounterKey) => (
    <td key={key} className={cn(CELL, NUMBER, row[key] === 0 ? "text-ink-3" : "text-ink")}>
      {key === "awayMs" ? readableDuration(row.awayMs / 1000) : row[key]}
    </td>
  );

  return (
    // The fill shows through transparent cells; the opaque name cell takes it
    // too.
    <tr
      aria-rowindex={index + 2}
      data-row={row.registrationId}
      data-fresh={fresh || undefined}
      className="group transition-colors duration-(--t-state) ease-standard data-fresh:bg-accent-wash"
      style={{ height: `${ROSTER_ROW_REM}rem` }}
    >
      <td
        className={cn(
          CELL,
          // Held at the left; opaque so numbers do not scroll through the name.
          "sticky left-0 z-1 bg-bg",
          NAME_WIDTH,
          "transition-colors duration-(--t-state) ease-standard group-data-fresh:bg-accent-wash",
        )}
      >
        <Link
          href={`/contests/${contestId}/monitor/${row.registrationId}`}
          className={cn(ONE_LINE, "block truncate text-control text-ink underline-offset-4 hover:underline")}
        >
          {row.fullName || row.login}
        </Link>
        <span className={cn(ONE_LINE, "block truncate font-mono text-label text-ink-3")}>{row.login}</span>
      </td>
      <td className={cn(CELL, "text-small whitespace-nowrap text-ink-2")}>{statuses[row.status] ?? row.status}</td>
      <td className={cn(CELL, "min-w-28")}>
        <FlagBadges flags={row.flags} t={t} />
      </td>
      {COUNTER_KEYS.map(count)}
      <td className={cn(CELL, NUMBER, "whitespace-nowrap text-ink-2")}>
        {row.lastActivity ? (
          <time dateTime={row.lastActivity} title={formatMoment(row.lastActivity, { locale })}>
            {formatTime(row.lastActivity, { locale })}
          </time>
        ) : (
          <span className="text-ink-3">{t.table.never}</span>
        )}
      </td>
    </tr>
  );
});

"use client";

import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { toCsv } from "@/lib/format/csv";
import type { PlayDictionary } from "./dictionary";
import { cn } from "@/lib/utils";

import type { ConsoleState } from "./actions";
import { PaneHandle, SHARE_BOUNDS, useResultRows } from "./pane-splitter";
import { RowDetail } from "./row-detail";
import { messageForCode } from "@/lib/i18n/errors";
import { refusalKind, showsReference } from "./refusals";

/**
 * The "Result" tab below the console: what the last query produced, or why
 * it did not. It renders the state `ConsoleEditor` reports through the
 * workspace, so the editor's form stays mounted outside every tab.
 */
export function ResultPanel({
  contestId,
  state,
  sourceTitle = null,
  dict,
}: {
  /** For the remembered height of the open-row split. */
  contestId: string;
  state: ConsoleState;
  /**
   * The name of the SQL tab the run came from, or null before any run. The
   * result stays while the participant types in another tab, so it says what
   * it answers. An empty name counts as none.
   */
  sourceTitle?: string | null;
  dict: PlayDictionary;
}) {
  if (state.kind === "idle") {
    return (
      <p className="p-4 text-body text-ink-2">{dict.participant.play.workspace.resultEmpty}</p>
    );
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {!sourceTitle ? null : (
        <p className="shrink-0 truncate border-b border-line px-3 py-1 text-small text-ink-3">
          {dict.participant.play.workspace.resultFrom.replace("{tab}", sourceTitle)}
        </p>
      )}
      <ResultBody contestId={contestId} state={state} dict={dict} />
    </div>
  );
}

/** The answer itself — a table, a count, or why the query did not run. */
function ResultBody({
  contestId,
  state,
  dict,
}: {
  contestId: string;
  state: Exclude<ConsoleState, { kind: "idle" }>;
  dict: PlayDictionary;
}) {
  const t = dict.participant.console;

  if (state.kind === "refused") {
    return (
      <div className="p-4">
        <Refusal state={state} dict={dict} />
      </div>
    );
  }

  const { result } = state;

  if (result.columns.length === 0) {
    // A write answers with a count rather than with rows.
    return (
      <p role="status" className="p-4 text-body text-ink">
        {t.affected.replace("{count}", String(result.rows_affected))}
      </p>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* The meter row between the editor and the table: a run's facts in one
          quiet line. */}
      <div className="flex shrink-0 flex-wrap items-center gap-4 border-b border-line px-3 py-1.5 font-mono text-label text-ink-3 uppercase">
        {/* The verdict, in the palette's good colour. */}
        <span className="flex items-center gap-1.5 text-good normal-case">
          <span aria-hidden="true" className="size-1.5 rounded-full bg-good" />
          {t.meter.ok}
        </span>
        <span>
          {t.meter.rows} <b className="font-medium text-ink tabular-nums">{result.rows.length}</b>
        </span>
        {/* Absent, not zero, when the statement was not timed: an unmeasured
            duration and a measured 0 ms are different facts. */}
        {result.duration_micros !== undefined ? (
          <span>
            {t.meter.time}{" "}
            <b className="font-medium text-ink tabular-nums">
              {t.meter.ms.replace("{n}", String(Math.max(1, Math.round(result.duration_micros / 1000))))}
            </b>
          </span>
        ) : null}
        <div className="flex-1" />
        <DownloadButton columns={result.columns} rows={result.rows} label={dict.participant.play.workspace.download} />
      </div>

      <div className="flex min-h-0 flex-1 flex-col gap-2 p-3">
        {result.truncated ? (
          <p role="status" className="shrink-0 text-small text-warn">
            {t.truncated.replace("{count}", String(result.rows.length))}
          </p>
        ) : null}

        {/* No box: the rows carry their own rules and the pane is bounded by the
            ones between the panes. */}
        <SelectableRows
          contestId={contestId}
          columns={result.columns}
          columnTypes={result.column_types}
          rows={result.rows}
          dict={dict}
        />

        {result.rows.length === 0 ? (
          <p className="shrink-0 text-small text-ink-2">{t.noRows}</p>
        ) : null}
      </div>
    </div>
  );
}

type Selection = {
  selected: number | null;
  /**
   * The row the keyboard is on, open or not. It holds the table's one tab
   * stop, so tabbing back into the table returns there.
   */
  cursor: number;
  onSelect: (index: number) => void;
  onRowKeyDown: (event: React.KeyboardEvent, index: number) => void;
};

/**
 * The table, the row open under it, and the edge between them (SPEC.md §5).
 *
 * The selection is a row number: the table keeps only visible rows in the
 * DOM, so a selection held on a node would vanish when it scrolls away. The
 * state lives here, not in the memoised `ResultPanel`, so opening a row
 * re-renders this subtree alone.
 */
function SelectableRows({
  contestId,
  columns,
  columnTypes,
  rows,
  dict,
}: {
  contestId: string;
  columns: readonly string[];
  columnTypes?: readonly string[];
  rows: readonly (string | null)[][];
  dict: PlayDictionary;
}) {
  const [selected, setSelected] = useState<number | null>(null);
  // The keyboard position is separate from what is open: the arrows walk the
  // table without opening anything until a row is.
  const [cursor, setCursor] = useState(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  // The row a keyboard walk asked to reach, until it is focused. A click never
  // sets it.
  const pending = useRef<number | null>(null);
  // The table's window recomputation, borrowed; see `revealNow`.
  const measureRef = useRef<(() => void) | null>(null);
  const { containerRef, sizes, commit } = useResultRows(contestId);

  // A new answer closes the panel: row 4 of the old array means nothing in
  // the new one. Reset during render, React's pattern for resetting state on
  // a prop change.
  const [answer, setAnswer] = useState(rows);
  if (answer !== rows) {
    setAnswer(rows);
    setSelected(null);
    setCursor(0);
  }

  // A shorter answer under the same array identity would otherwise leave the
  // panel reading past its end.
  const open = selected !== null && selected < rows.length ? selected : null;

  /**
   * Scrolls to the row the keyboard asked for and focuses it if it is in the
   * DOM yet. Walking off the window's edge needs the borrowed `measure`: the
   * browser's scroll event comes a frame later, and without it the walk
   * stalls with nothing focused. If the row has not arrived, the render that
   * `measure` schedules runs this again.
   */
  const revealNow = useCallback(() => {
    const index = pending.current;
    const scroller = scrollRef.current;
    if (index === null) return;
    if (!scroller) {
      pending.current = null;
      return;
    }

    const height = rowHeightPx();
    const top = index * height;
    if (top < scroller.scrollTop) {
      scroller.scrollTop = top;
    } else if (top + height > scroller.scrollTop + scroller.clientHeight) {
      scroller.scrollTop = top + height - scroller.clientHeight;
    }
    measureRef.current?.();

    const row = scroller.querySelector<HTMLElement>(`tbody tr[aria-rowindex="${index + 2}"]`);
    if (row) {
      pending.current = null;
      row.focus();
    }
  }, [scrollRef]);

  const reveal = useCallback(
    (index: number) => {
      pending.current = index;
      revealNow();
    },
    [revealNow],
  );

  // No dependency list: it waits for the row to appear, which is a property
  // of the render. It returns at once when nothing is pending.
  useLayoutEffect(revealNow);

  const select = useCallback((index: number) => {
    setSelected(index);
    setCursor(index);
  }, []);

  const step = useCallback(
    (from: number, delta: number) => {
      const next = Math.min(rows.length - 1, Math.max(0, from + delta));
      // With a row open the arrows move the selection; with none open they
      // move only the cursor, so walking an answer opens nothing.
      if (open !== null) setSelected(next);
      setCursor(next);
      reveal(next);
    },
    [open, rows.length, reveal],
  );

  const close = useCallback(() => {
    // Focus returns to the row the participant was on.
    if (open !== null) reveal(open);
    setSelected(null);
  }, [open, reveal]);

  const onRowKeyDown = useCallback(
    (event: React.KeyboardEvent, index: number) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        select(index);
      } else if (event.key === "ArrowDown") {
        event.preventDefault();
        step(index, 1);
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        step(index, -1);
      } else if (event.key === "Escape") {
        close();
      }
    },
    [step, close, select],
  );

  const onPanelKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (open === null) return;
      if (event.key === "ArrowDown") {
        event.preventDefault();
        step(open, 1);
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        step(open, -1);
      } else if (event.key === "Escape") {
        close();
      }
    },
    [open, step, close],
  );

  const selection = useMemo<Selection>(
    () => ({ selected: open, cursor, onSelect: select, onRowKeyDown }),
    [open, cursor, select, onRowKeyDown],
  );

  return (
    <div
      ref={containerRef}
      style={{ "--pane-detail": `${sizes.table}%` } as React.CSSProperties}
      className={cn(
        // Below `narrow` this is a flex column, not a grid: the bottom panel has
        // only a `max-height` there, so a percentage track resolves as `auto` and
        // pushes the open row out of the clipped box. Each half of the flex column
        // scrolls inside itself.
        "grid min-h-0 flex-1 grid-cols-1 max-narrow:flex max-narrow:flex-col",
        open === null
          ? "grid-rows-1"
          : "grid-rows-[minmax(0,var(--pane-detail))_auto_minmax(0,1fr)]",
      )}
    >
      <ResultTable
        columns={columns}
        columnTypes={columnTypes}
        rows={rows}
        nullLabel={dict.participant.console.null}
        scrollRef={scrollRef}
        measureRef={measureRef}
        selection={selection}
      />
      {open === null ? null : (
        <>
          <PaneHandle
            label={dict.participant.play.workspace.panes.detail}
            property="--pane-detail"
            value={sizes.table}
            axis="y"
            unit="%"
            bounds={SHARE_BOUNDS}
            direction={1}
            containerRef={containerRef}
            onResize={(share) => commit({ table: share })}
            // Below `narrow` the two are stacked at stated heights, not a share.
            className="max-narrow:hidden"
          />
          <RowDetail
            columns={columns}
            columnTypes={columnTypes}
            row={rows[open]}
            index={open}
            nullLabel={dict.participant.console.null}
            dict={dict}
            onClose={close}
            onKeyDown={onPanelKeyDown}
            // Sizes this half of the flex column below `narrow`; inert in the grid.
            className="max-narrow:flex-1"
          />
        </>
      )}
    </div>
  );
}

/**
 * Every row is exactly this tall, which makes the window arithmetic rather
 * than measurement: the 17px body line at 1.65 plus `p-2` above and below.
 * In rem so rows grow with the browser's text size.
 */
const ROW_REM = 2.75;

/**
 * Row height in pixels, from the root font size. Read when needed rather than
 * kept: `getComputedStyle` flushes style, and neither caller is per-event.
 */
function rowHeightPx(): number {
  return ROW_REM * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
}

/**
 * Rows rendered beyond the visible ones, so a trackpad flick never shows a
 * blank band before the next render.
 */
const OVERSCAN = 8;

/**
 * Rows rendered before the container is measured (first render, and on the
 * server). The layout effect corrects it before paint.
 */
const UNMEASURED_ROWS = 40;

const WIDTH_SAMPLE = 50;
/**
 * Column width bounds, in characters of the mono face. 38 fits a `uuid` plus
 * padding; longer values are clipped, with the full text in the cell's
 * `title` and in the CSV.
 */
const MIN_COL_CH = 6;
const MAX_COL_CH = 38;

/**
 * Column widths from the header and a sample of the rows, clamped at both
 * ends. `table-layout: fixed` needs them stated, and fixed layout is what
 * gives every row a known height for the window. Sampling fifty rows from
 * memory costs the same whatever the answer's size; an even split would give
 * an `id` as much room as a witness statement.
 */
function columnWidths(columns: readonly string[], rows: readonly (string | null)[][]): number[] {
  const widths = columns.map((column) =>
    Math.min(MAX_COL_CH, Math.max(MIN_COL_CH, column.length + 2)),
  );

  const sample = Math.min(rows.length, WIDTH_SAMPLE);
  for (let r = 0; r < sample; r += 1) {
    const row = rows[r];
    for (let c = 0; c < widths.length; c += 1) {
      const value = row[c];
      // A null prints as the word, so the word has to fit.
      const length = (value ?? "NULL").length + 2;
      if (length > widths[c]) widths[c] = Math.min(MAX_COL_CH, length);
    }
  }

  return widths;
}

type RowWindow = { start: number; end: number };

/**
 * The result table: fixed layout, fixed row height, and only the rows the
 * pane can show in the DOM.
 *
 * A thousand-row, eight-column answer (the runner's `QUERY_MAX_ROWS` cap,
 * and a typical first `SELECT *`) took about 40ms to lay out on a fast
 * machine and several times that on contest laptops; windowed it renders
 * about 150 cells in under a millisecond. Hand-written because a windowing
 * library's features (variable heights, horizontal windowing) are unused
 * here and would add weight to this route's client bundle.
 *
 * Scrolling goes through React, since it changes which rows exist; the state
 * is a pair of integers and React skips the render when neither moved.
 */
function ResultTable({
  columns,
  columnTypes,
  rows,
  nullLabel,
  scrollRef,
  measureRef,
  selection,
}: {
  columns: readonly string[];
  columnTypes?: readonly string[];
  rows: readonly (string | null)[][];
  nullLabel: string;
  /** The scroll box, owned above so a keyboard walk can scroll to its row. */
  scrollRef: React.RefObject<HTMLDivElement | null>;
  /** Lends this table's window recomputation to that walk; see `revealNow`. */
  measureRef: React.RefObject<(() => void) | null>;
  selection: Selection;
}) {
  const rowHeightRef = useRef(ROW_REM * 16);
  const [rowWindow, setRowWindow] = useState<RowWindow>({
    start: 0,
    end: Math.min(rows.length, UNMEASURED_ROWS),
  });

  const total = rows.length;

  const measure = useCallback(() => {
    const element = scrollRef.current;
    if (!element) return;

    const rowHeight = rowHeightRef.current;
    const first = Math.max(0, Math.floor(element.scrollTop / rowHeight));
    const fits = Math.ceil(element.clientHeight / rowHeight);
    const start = Math.max(0, first - OVERSCAN);
    const end = Math.min(total, first + fits + OVERSCAN);

    // Same pair, no render: a scroll inside one row costs nothing.
    setRowWindow((previous) => (previous.start === start && previous.end === end ? previous : { start, end }));
    // `scrollRef` is stable; named because the lint rule cannot see that
    // through a prop.
  }, [total, scrollRef]);

  // A new answer starts at its first row. Reset during render rather than
  // in an effect, which would render the table twice.
  const [answer, setAnswer] = useState(rows);
  if (answer !== rows) {
    setAnswer(rows);
    setRowWindow({ start: 0, end: Math.min(rows.length, UNMEASURED_ROWS) });
  }

  // A child's layout effect runs before its parent's, so `revealNow` finds
  // this in place.
  useLayoutEffect(() => {
    measureRef.current = measure;
    return () => {
      measureRef.current = null;
    };
  }, [measure, measureRef]);

  useLayoutEffect(() => {
    rowHeightRef.current = rowHeightPx();

    // The scroll offset survives a new answer unless reset here.
    const element = scrollRef.current;
    if (element) element.scrollTop = 0;
    measure();

    // The pane is resized by the dividers and the window. `ResizeObserver` is
    // absent in jsdom.
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
    // `rows` is named because the scroll reset is about the array, not its
    // length.
  }, [rows, measure, scrollRef]);

  const widths = useMemo(() => columnWidths(columns, rows), [columns, rows]);
  const tableWidth = widths.reduce((sum, width) => sum + width, 0);

  const start = Math.min(rowWindow.start, Math.max(0, total));
  const end = Math.max(start, Math.min(rowWindow.end, total));

  // The one row in the tab order: the keyboard's row while it is rendered,
  // otherwise the first rendered row.
  const anchor = selection.selected ?? selection.cursor;
  const tabRow = anchor >= start && anchor < end ? anchor : start;

  return (
    // `flex-1` gives this its share of the column below `narrow`; inert in the
    // grid.
    <div ref={scrollRef} onScroll={measure} className="min-h-0 flex-1 overflow-auto">
      {/* `table-fixed` keeps every row a known height, and `font-mono` on the
          table lets the widths be stated in `ch`, which resolves against the
          element it is written on. `aria-rowcount` and each row's
          `aria-rowindex` report the whole answer while only part is in the
          DOM; the header is row 1, so a data row is its offset plus two. */}
      <table
        className="w-full table-fixed border-collapse font-mono text-body"
        style={{ minWidth: `${tableWidth}ch` }}
        aria-rowcount={total + 1}
      >
        <colgroup>
          {widths.map((width, i) => (
            <col key={i} style={{ width: `${width}ch` }} />
          ))}
        </colgroup>
        {/* Opaque, or the rows would show through the sticky head. */}
        <thead className="sticky top-0 z-1 bg-bg">
          <tr aria-rowindex={1} className="border-b border-edge">
            {columns.map((column, i) => (
              <th
                key={`${column}-${i}`}
                title={column}
                className="truncate p-2 text-left align-bottom text-label text-ink-3 uppercase"
              >
                {column}
                {/* The type under the name. A column whose type is unknown shows
                    nothing rather than a wrong label. */}
                {columnTypes?.[i] ? (
                  <span className="block truncate font-normal normal-case">{columnTypes[i]}</span>
                ) : null}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {/* Rows outside the window, as height, so the scrollbar reflects the
              whole answer. In rem, like the rows. */}
          {start > 0 ? (
            <tr aria-hidden="true" style={{ height: `${start * ROW_REM}rem` }}>
              <td colSpan={columns.length} />
            </tr>
          ) : null}
          {rows.slice(start, end).map((row, i) => {
            const index = start + i;
            return (
            <tr
              key={index}
              aria-rowindex={index + 2}
              // ARIA 1.2 lets a table row be selected, so this stays an ordinary
              // `table` rather than a grid.
              aria-selected={selection.selected === index}
              // Exactly one row is tabbable, so Tab reaches the table once and the
              // arrows take it from there.
              tabIndex={index === tabRow ? 0 : -1}
              onClick={() => selection.onSelect(index)}
              onKeyDown={(event) => selection.onRowKeyDown(event, index)}
              // A fixed height is what the window is computed from, so cells clip
              // rather than wrap.
              style={{ height: `${ROW_REM}rem` }}
              className={cn(
                "border-b border-edge outline-none",
                selection.selected === index
                  ? "bg-accent-wash"
                  : "hover:bg-sunk focus-visible:bg-sunk",
              )}
            >
              {columns.map((_, c) => {
                const cell = row[c] ?? null;
                return (
                  <td
                    key={c}
                    // The whole value, for a column too narrow to show it.
                    title={cell ?? undefined}
                    className="truncate p-2 align-top text-ink"
                  >
                    {/* NULL and the empty string differ in SQL, and a participant
                        debugging a left join is looking at exactly that. */}
                    {cell === null ? <span className="text-ink-2 italic">{nullLabel}</span> : cell}
                  </td>
                );
              })}
            </tr>
            );
          })}
          {end < total ? (
            <tr aria-hidden="true" style={{ height: `${(total - end) * ROW_REM}rem` }}>
              <td colSpan={columns.length} />
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}

/**
 * Builds the CSV in the browser from the rows already on screen; no second
 * request. The runner's budget already truncated them, so the file never
 * carries more than the table showed. The object URL is revoked after the
 * click.
 */
function DownloadButton({
  columns,
  rows,
  label,
}: {
  columns: readonly string[];
  rows: readonly (string | null)[][];
  label: string;
}) {
  return (
    <Button
      type="button"
      variant="quiet"
      size="sm"
      onClick={() => {
        const csv = toCsv(columns, rows);
        const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
        const link = document.createElement("a");
        link.href = url;
        link.download = `query-result-${Date.now()}.csv`;
        link.click();
        URL.revokeObjectURL(url);
      }}
    >
      {label}
    </Button>
  );
}

/**
 * Why a query did not run: the dictionary's sentence for the code, with the
 * subject appended so the participant knows which function or table it means.
 */
function Refusal({ state, dict }: { state: Extract<ConsoleState, { kind: "refused" }>; dict: PlayDictionary }) {
  const message = messageForCode(state.code, dict.errors);

  // Waiting differs from being wrong: one means try again in a moment, the
  // other means change the query.
  const passing = refusalKind(state.code) === "passing";

  return (
    <div
      role="status"
      className={cn(
        "border p-3 text-body",
        passing ? "border-line-2 bg-sunk text-ink" : "border-bad/40 bg-bad-wash text-ink",
      )}
    >
      {message}
      {state.subject ? <span className="ml-1 font-mono text-ink-2">{state.subject}</span> : null}
      {/* Only for faults on our side, the one case where anybody will ask for
          it; under an ordinary refusal it would read as a fault. */}
      {state.requestId && showsReference(state.code, dict.errors) ? (
        <p className="mt-2 font-mono text-small text-ink-3">
          {dict.participant.console.reference.replace("{id}", state.requestId)}
        </p>
      ) : null}
    </div>
  );
}

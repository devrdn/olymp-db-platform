"use client";

import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { toCsv } from "@/lib/format/csv";
import type { PlayDictionary } from "./dictionary";
import { cn } from "@/lib/utils";

import type { ConsoleState } from "./actions";

/**
 * What the last query produced, or why it did not — the "Result" tab of the
 * panel below the console.
 *
 * Reads `state` from the workspace rather than owning `useActionState`
 * itself: the form and its action live in `ConsoleEditor`, which stays
 * mounted and unchanged across a run so the textarea never remounts; this
 * component only renders whatever `ConsoleEditor` last reported. Being a
 * separate component — rather than folding this rendering back into the
 * editor — is what lets the workspace put it inside a *tab*, hidden without
 * unmounting when "Query log" is the one showing, while `ConsoleEditor`
 * itself stays outside every tab.
 */
export function ResultPanel({ state, dict }: { state: ConsoleState; dict: PlayDictionary }) {
  const t = dict.participant.console;

  if (state.kind === "idle") {
    return (
      <p className="p-4 text-body text-ink-2">{dict.participant.play.workspace.resultEmpty}</p>
    );
  }
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
      {/* The meter row the design puts between the editor and the table
          (docs/design/preview.html, "SQL-консоль"): a run's own facts, in one
          quiet line, rather than a sentence per fact above the data. */}
      <div className="flex shrink-0 flex-wrap items-center gap-4 border-b border-line px-3 py-1.5 font-mono text-label text-ink-3 uppercase">
        {/* The run's own verdict, in the two colours the palette keeps for
            exactly this (good / bad). A participant should be able to tell a
            query that worked from one that did not without reading the row —
            and until now they could not tell them apart at all, because the
            failing branch was painted in a colour this design system does not
            have. */}
        <span className="flex items-center gap-1.5 text-good normal-case">
          <span aria-hidden="true" className="size-1.5 rounded-full bg-good" />
          {t.meter.ok}
        </span>
        <span>
          {t.meter.rows} <b className="font-medium text-ink tabular-nums">{result.rows.length}</b>
        </span>
        {/* Absent, not zero, when the answer did not come from a statement
            this build timed — an unmeasured duration and a measured 0 ms are
            different facts, and printing "0 мс" for the first reads as a
            broken meter. */}
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

        {/* No box: the rows carry their own rules, and the pane is already
            bounded by the ones between the panes (preview.html, `table.dt`). */}
        <ResultTable
          columns={result.columns}
          columnTypes={result.column_types}
          rows={result.rows}
          nullLabel={t.null}
        />

        {result.rows.length === 0 ? (
          <p className="shrink-0 text-small text-ink-2">{t.noRows}</p>
        ) : null}
      </div>
    </div>
  );
}

/**
 * Every row is exactly this tall, and that is what makes the window below
 * arithmetic rather than measurement: `2.75rem` is the 17px body line at
 * 1.65 plus the `p-2` above and below it, which is the height the rows
 * already had when they sized themselves.
 *
 * A rem rather than a pixel count, so a reader who enlarged their browser's
 * text still gets rows their text fits in. The pixel value is derived once
 * per mount from the root font size — the same read `pane-splitter.tsx` does
 * once per drag, and for the same reason: `getComputedStyle` is a
 * synchronous style flush, and the root font size cannot change under a
 * query's result.
 */
const ROW_REM = 2.75;

/**
 * Rows kept rendered above and below the ones actually on screen, so a flick
 * of a trackpad — which scrolls further than one frame's worth of scroll
 * events reports — never shows a blank band before the next render lands.
 */
const OVERSCAN = 8;

/**
 * How many rows exist before the container has been measured: the very first
 * render, and every render on a server. Enough to fill a tall pane, few
 * enough to cost nothing — the layout effect below replaces it with the truth
 * before the browser paints.
 */
const UNMEASURED_ROWS = 40;

/** How many rows are read to decide the column widths. */
const WIDTH_SAMPLE = 50;
/**
 * A column is never narrower than this, nor wider, in characters of the mono
 * face.
 *
 * The ceiling is 38 because a `uuid` is 36 characters plus this table's own
 * padding, and half a primary key is not something a participant can carry
 * into the next query. Past that a value is clipped and its whole text is on
 * the cell's `title` (and in the CSV beside the table).
 */
const MIN_COL_CH = 6;
const MAX_COL_CH = 38;

/**
 * The column widths, decided here rather than by the browser.
 *
 * This is what `table-layout: fixed` costs and what makes it worth paying.
 * Automatic layout measures every cell before it can place the first one;
 * fixed layout reads the widths it is given and stops. Measured on a
 * 1000×8 answer, that trade on its own is small — 37.9ms against 40.1ms,
 * because the bulk of an unwindowed table's cost is having eight thousand
 * cells at all, not deciding how wide they are. It is here for the other
 * reason: fixed layout is what makes a row a known height, and a known row
 * height is what turns the window below into arithmetic.
 *
 * Somebody then has to decide how wide a column is, and dividing the pane
 * equally would give an `id` the same room as a witness statement.
 *
 * So: the widths come from the header plus a sample of the rows, clamped at
 * both ends. Fifty rows is a constant amount of work whatever the answer's
 * size, and it is read from an array that is already in memory — no layout,
 * no text metrics. A value longer than the clamp is clipped with an ellipsis
 * and carries its full text in a `title`, the way every SQL client shows a
 * column too narrow for its contents; the CSV beside the table is what
 * carries the untruncated value.
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
      // A null prints as the word, so it is the word that has to fit.
      const length = (value ?? "NULL").length + 2;
      if (length > widths[c]) widths[c] = Math.min(MAX_COL_CH, length);
    }
  }

  return widths;
}

/** The rows currently worth having in the DOM, as a half-open range. */
type RowWindow = { start: number; end: number };

/**
 * The result table: fixed layout, fixed row height, and only the rows the
 * pane can actually show.
 *
 * The measurement this replaces (finding 1): building and laying out a
 * thousand-row, eight-column answer took 40.1ms on an M-series machine
 * before React had reconciled anything — 3.1ms of building the nodes and
 * 37.1ms of laying out the eight thousand cells — and three to five times
 * that on the laptops this contest is actually sat in front of, which is a
 * tab frozen for up to a second on a screen with a clock running in the
 * corner. The same answer through this table is 0.6ms and 153 cells. About
 * twenty of those thousand rows are on screen at a time. The runner already
 * caps an answer at `QUERY_MAX_ROWS` (1000), and a participant's first query
 * is `SELECT * FROM …` more often than not, so this is the ordinary path
 * rather than the pathological one.
 *
 * Written here rather than taken from a library on purpose: what a windowing
 * library buys is variable row heights, horizontal windowing and a scroll
 * anchoring model, none of which this table has any use for — every row is
 * the same height by construction, and the widest answer this runner returns
 * is a few dozen columns. What it would cost is a dependency in the client
 * graph of the one route whose weight matters most in this product, which is
 * the same trade `components/ui/tabs.tsx` already refused for the same
 * screen.
 *
 * The scroll handler does not write a style, so unlike `pane-splitter.tsx`
 * this one does go through React: the whole point is to change *which* rows
 * exist. What keeps that cheap is that the state is a pair of integers and
 * settles to the same pair for most events — React bails out of a render
 * when neither number moved, which is every scroll event that stays inside
 * the row it started in.
 */
function ResultTable({
  columns,
  columnTypes,
  rows,
  nullLabel,
}: {
  columns: readonly string[];
  columnTypes?: readonly string[];
  rows: readonly (string | null)[][];
  nullLabel: string;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
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

    // Same pair, same render: this is what makes a scroll event that stayed
    // inside one row cost nothing at all.
    setRowWindow((previous) => (previous.start === start && previous.end === end ? previous : { start, end }));
  }, [total]);

  // A new answer is read from its first row, so the window goes back to the
  // top the moment `rows` is a different array. Adjusted during the render
  // that brings the new answer in rather than in an effect afterwards: this
  // is React's own "reset state when a prop changes" pattern, and the effect
  // version of it is a second render of the table for nothing.
  const [answer, setAnswer] = useState(rows);
  if (answer !== rows) {
    setAnswer(rows);
    setRowWindow({ start: 0, end: Math.min(rows.length, UNMEASURED_ROWS) });
  }

  useLayoutEffect(() => {
    rowHeightRef.current =
      ROW_REM * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);

    // The scroll offset is the browser's, not React's, and it survives a new
    // answer: without this a shorter result opens somewhere in the middle of
    // itself, and a longer one opens at rows nobody asked to skip to.
    const element = scrollRef.current;
    if (element) element.scrollTop = 0;
    measure();

    // The pane this table sits in is resized by two draggable dividers and by
    // the window itself, and a taller pane needs more rows in it.
    // `ResizeObserver` is absent in jsdom, where there is no layout to observe.
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
    // `measure` changes identity with the row count, so a new answer re-runs
    // this on its own — `rows` is named too because the scroll reset is about
    // the array, not about how long it is.
  }, [rows, measure]);

  const widths = useMemo(() => columnWidths(columns, rows), [columns, rows]);
  const tableWidth = widths.reduce((sum, width) => sum + width, 0);

  const start = Math.min(rowWindow.start, Math.max(0, total));
  const end = Math.max(start, Math.min(rowWindow.end, total));

  return (
    <div ref={scrollRef} onScroll={measure} className="min-h-0 flex-1 overflow-auto">
      {/* `table-fixed` is what makes every row a known height, which is what
          the window is computed from (see `columnWidths`), and `font-mono` on
          the table itself is what lets those widths be stated in `ch`: a `ch`
          is resolved against the element it is written on, and the cells are
          the mono ones.

          `aria-rowcount` is how a table that has only some of its rows in the
          DOM still tells the truth about its size — the ARIA contract for
          exactly this, with `aria-rowindex` on each row saying which row of
          the whole answer it is. The header is row 1, so a data row's index is
          its offset plus two. */}
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
        {/* Opaque, and in a colour this design system actually has: a sticky
            head with no fill is a head the rows scroll through. */}
        <thead className="sticky top-0 z-1 bg-bg">
          <tr aria-rowindex={1} className="border-b border-edge">
            {columns.map((column, i) => (
              <th
                key={`${column}-${i}`}
                title={column}
                className="truncate p-2 text-left align-bottom text-label text-ink-3 uppercase"
              >
                {column}
                {/* The type under the name, the way the design draws it. A
                    column whose type this build could not name simply has
                    nothing under it — better a missing label than a wrong
                    one (see the handler's column_types contract). */}
                {columnTypes?.[i] ? (
                  <span className="block truncate font-normal normal-case">{columnTypes[i]}</span>
                ) : null}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {/* The rows above and below the window, as height rather than as
              elements: this is what keeps the scrollbar the size the whole
              answer would make it, so the thumb means what it looks like it
              means. Stated in the same rem as the rows themselves, so nothing
              here depends on the pixel value read in the effect above. */}
          {start > 0 ? (
            <tr aria-hidden="true" style={{ height: `${start * ROW_REM}rem` }}>
              <td colSpan={columns.length} />
            </tr>
          ) : null}
          {rows.slice(start, end).map((row, i) => (
            <tr
              key={start + i}
              aria-rowindex={start + i + 2}
              // The height is the contract the window is computed from, not a
              // decoration: a row that grew to fit its content would put every
              // row after it at an offset this arithmetic does not know about.
              // That is why the cells clip rather than wrap.
              style={{ height: `${ROW_REM}rem` }}
              className="border-b border-edge"
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
                    {/* NULL and the empty string are different things in SQL,
                        and a participant debugging a left join is looking at
                        exactly that column. */}
                    {cell === null ? <span className="text-ink-2 italic">{nullLabel}</span> : cell}
                  </td>
                );
              })}
            </tr>
          ))}
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
 * Builds the CSV client-side, from exactly the columns and rows already on
 * screen, and hands it to the browser — no second request, no server
 * involvement. That is also the boundary the plan draws (Task 3): the
 * runner's own budget already truncated `result.rows` before this component
 * ever saw it, so there is no way for this button to carry more than the
 * table above it already showed.
 *
 * `URL.createObjectURL` plus a click on a detached `<a download>` is the
 * ordinary way to save a browser-generated file without a round trip; the
 * object URL is revoked right after, so nothing here holds memory past the
 * click that used it.
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
 * Why a query did not run.
 *
 * The sentence comes from the dictionary by code and the subject is appended,
 * because "that function is not available" without naming it is the same
 * unactionable answer whichever language it is in.
 */
function Refusal({ state, dict }: { state: Extract<ConsoleState, { kind: "refused" }>; dict: PlayDictionary }) {
  const errors = dict.errors as Record<string, string>;
  const message = errors[state.code] ?? errors.fallback;

  // Waiting is a different situation from being wrong, and the participant
  // should be able to tell without reading carefully: one of these means try
  // again in a moment, the other means change the query.
  const passing = ["query_busy", "query_already_running", "query_too_often", "no_game_yet"].includes(
    state.code,
  );

  return (
    <div
      role="status"
      className={cn(
        "border p-3 text-body",
        // `bad` and `bad-wash`, which are in the palette — the previous
        // `danger` was in no stylesheet at all, so Tailwind emitted nothing
        // and a refused query and a "try again in a moment" looked identical.
        passing ? "border-line-2 bg-sunk text-ink" : "border-bad/40 bg-bad-wash text-ink",
      )}
    >
      {message}
      {state.subject ? <span className="ml-1 font-mono text-ink-2">{state.subject}</span> : null}
      {/* Shown only where something went wrong on our side, because that is
          the only case where anybody will be asked for it — and a reference
          number printed under an ordinary refusal reads as though the refusal
          were a fault. */}
      {state.requestId && !passing ? (
        <p className="mt-2 font-mono text-small text-ink-3">
          {dict.participant.console.reference.replace("{id}", state.requestId)}
        </p>
      ) : null}
    </div>
  );
}

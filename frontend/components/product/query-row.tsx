"use client";

import { memo, useId, useState } from "react";

import type { LoggedQuery } from "@/lib/api/journal";
import { formatMoment, formatSeconds } from "@/lib/format/datetime";
import { cn } from "@/lib/utils";

import { CopyButton, SqlBlock } from "./sql-block";

/** Status colours; running is the one live tone. */
export const QUERY_TONE: Record<string, string> = {
  running: "text-accent",
  ok: "text-good",
  error: "text-bad",
  rejected: "text-warn",
  timeout: "text-warn",
};

/**
 * Labels for one query row, passed in rather than read from the dictionary:
 * staff and the participant see these rows in different words.
 */
export type QueryRowLabels = {
  durationMs: string;
  rows: string;
  show: string;
  hide: string;
  copy: string;
  copied: string;
  copyFailed: string;
  shortened: string;
  /** Only needed where the address is shown. */
  noAddress?: string;
};

/**
 * One query: time, outcome, duration, rows, first line and error; expanded, the
 * whole statement with a copy button. The address is a staff column, so the
 * caller decides.
 *
 * Memoised on the query with its own expansion state, so a refresh or an
 * expansion re-renders one row. `content-visibility` lets the browser skip
 * off-screen rows without a fixed-height window.
 */
export const QueryRow = memo(function QueryRow({
  query,
  labels,
  statuses,
  locale,
  address = false,
}: {
  query: LoggedQuery;
  labels: QueryRowLabels;
  /** Each status in words; an unknown one is shown as it came. */
  statuses: Record<string, string>;
  locale: string;
  address?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const bodyId = useId();
  const first = query.sql.split("\n").find((line) => line.trim() !== "") ?? query.sql;

  return (
    <li className="flex min-w-0 flex-col gap-1 border-b border-line py-2 [contain-intrinsic-size:auto_4.5rem] [content-visibility:auto]">
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1">
        <time
          dateTime={query.executedAt}
          title={formatMoment(query.executedAt, { locale })}
          className="shrink-0 font-mono text-label text-ink-3 tabular-nums"
        >
          {formatSeconds(query.executedAt, { locale })}
        </time>
        <span className={cn("font-mono text-label uppercase", QUERY_TONE[query.status] ?? "text-ink-3")}>
          {statuses[query.status] ?? query.status}
        </span>
        {query.durationMs !== null ? (
          <span className="font-mono text-label text-ink-2 tabular-nums">
            {labels.durationMs.replace("{n}", String(query.durationMs))}
          </span>
        ) : null}
        {query.rowCount !== null ? (
          <span className="font-mono text-label text-ink-2 tabular-nums">
            {labels.rows.replace("{n}", String(query.rowCount))}
          </span>
        ) : null}
        {address ? (
          <span className="min-w-0 truncate font-mono text-label text-ink-3">{query.ip ?? labels.noAddress}</span>
        ) : null}
        <button
          type="button"
          aria-expanded={open}
          aria-controls={bodyId}
          onClick={() => setOpen((current) => !current)}
          className="ml-auto shrink-0 text-small text-ink-2 underline-offset-4 hover:text-ink hover:underline"
        >
          {open ? labels.hide : labels.show}
        </button>
      </div>
      {open ? null : <p className="min-w-0 truncate font-mono text-data text-ink-2">{first}</p>}
      {query.error ? <p className="min-w-0 text-small break-words text-bad">{query.error}</p> : null}
      <div id={bodyId} className="flex min-w-0 flex-col gap-2 empty:hidden">
        {open ? (
          <>
            <SqlBlock sql={query.sql} />
            <div className="flex flex-wrap items-center gap-3">
              <CopyButton text={query.sql} labels={labels} />
              {query.sqlTruncated ? <p className="text-small text-warn">{labels.shortened}</p> : null}
            </div>
          </>
        ) : null}
      </div>
    </li>
  );
});

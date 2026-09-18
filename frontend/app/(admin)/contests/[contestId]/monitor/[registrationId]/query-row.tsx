"use client";

import { memo, useId, useState } from "react";

import type { LoggedQuery } from "@/lib/api/monitor";
import { formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { clock, QUERY_TONE } from "../live-feed";
import { CopyButton, SqlBlock } from "./sql-block";

/**
 * One query, as the queries tab and the answers tab list it: when, how it
 * ended, how long, how many rows, from which address, its first line and
 * its error; expanded, the whole statement read-only with a copy button.
 *
 * Memoised on the query object, and its expansion is its own state: opening
 * one statement renders one row, and a refresh that changed another query
 * leaves this one alone. `content-visibility` lets the browser skip laying
 * out and painting rows out of view, which keeps a long list of rows of
 * uneven height cheap without a fixed-height window.
 */
export const QueryRow = memo(function QueryRow({
  query,
  dict,
  locale,
}: {
  query: LoggedQuery;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor.participant.queries;
  const statuses = dict.workspace.monitor.feed.queryStatus as Record<string, string>;
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
          {clock(query.executedAt, locale)}
        </time>
        <span className={cn("font-mono text-label uppercase", QUERY_TONE[query.status] ?? "text-ink-3")}>
          {statuses[query.status] ?? query.status}
        </span>
        {query.durationMs !== null ? (
          <span className="font-mono text-label text-ink-2 tabular-nums">
            {t.durationMs.replace("{n}", String(query.durationMs))}
          </span>
        ) : null}
        {query.rowCount !== null ? (
          <span className="font-mono text-label text-ink-2 tabular-nums">
            {t.rows.replace("{n}", String(query.rowCount))}
          </span>
        ) : null}
        <span className="min-w-0 truncate font-mono text-label text-ink-3">{query.ip ?? t.noAddress}</span>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={bodyId}
          onClick={() => setOpen((current) => !current)}
          className="ml-auto shrink-0 text-small text-ink-2 underline-offset-4 hover:text-ink hover:underline"
        >
          {open ? t.hide : t.show}
        </button>
      </div>
      {open ? null : <p className="min-w-0 truncate font-mono text-data text-ink-2">{first}</p>}
      {query.error ? <p className="min-w-0 text-small break-words text-bad">{query.error}</p> : null}
      <div id={bodyId} className="flex min-w-0 flex-col gap-2 empty:hidden">
        {open ? (
          <>
            <SqlBlock sql={query.sql} />
            <div className="flex flex-wrap items-center gap-3">
              <CopyButton text={query.sql} labels={t} />
              {query.sqlTruncated ? <p className="text-small text-warn">{t.shortened}</p> : null}
            </div>
          </>
        ) : null}
      </div>
    </li>
  );
});

"use client";

import { useCallback, useEffect, useId, useRef, useState } from "react";

import { ExportMenu } from "@/components/product/export-menu";
import { CopyButton, SqlBlock } from "@/components/product/sql-block";
import { buttonVariants } from "@/components/ui/button";
import { API_PREFIX } from "@/lib/api/client";
import { QUERY_LOG_PAGE_SIZE, QUERY_LOG_REFRESH_MIN_INTERVAL_MS } from "@/lib/api/querylog-terms";
import type { QueryLogEntry } from "@/lib/api/querylog";
import { formatMoment } from "@/lib/format/datetime";
import type { PlayDictionary } from "./dictionary";
import type { Locale } from "@/lib/i18n/config";
import { messageForCode } from "@/lib/i18n/errors";
import { cn } from "@/lib/utils";

import { fetchQueryLog, fetchQueryLogEntry } from "./requests";

/**
 * The "Query log" tab: every statement this participant has run in this
 * contest, newest first, surviving a reload.
 *
 * `initial` comes from page.tsx's server-side read; `initial.failed` marks a
 * read that failed, so the panel shows the failure with a retry instead of
 * the "no queries yet" an empty log shows.
 *
 * The panel stays mounted and refreshes on becoming `active`, not after each
 * run: its reads (`AdmitRead`) spend the play screen's per-minute budget.
 *
 * It loads `QUERY_LOG_PAGE_SIZE` rows at a time (querylog-terms.ts), so a
 * long log is not hundreds of rows re-rendered per page. Each row opens to
 * its whole statement (`LogRow`).
 */
export function QueryLogPanel({
  contestId,
  initial,
  active,
  locale,
  dict,
}: {
  contestId: string;
  initial: { items: QueryLogEntry[]; total: number; failed: boolean };
  /** Whether the "Query log" tab is showing. */
  active: boolean;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.log;
  const [items, setItems] = useState(initial.items);
  const [total, setTotal] = useState(initial.total);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failed, setFailed] = useState(initial.failed);

  // Two guards on the on-show refresh. `requestSeq` numbers each refresh,
  // and only the latest one's answer is applied, since a slow response can
  // overlap a newer one. `lastRefreshAt` keeps toggling between tabs from
  // spending a read within `QUERY_LOG_REFRESH_MIN_INTERVAL_MS`; `retry` is
  // an explicit click and bypasses it.
  //
  // `refresh` depends on `items.length` (the page size to re-read); a new
  // identity re-runs the effect below, which calls nothing without a real
  // transition.
  const requestSeq = useRef(0);
  const lastRefreshAt = useRef(0);
  const refresh = useCallback(async () => {
    const requestId = ++requestSeq.current;
    lastRefreshAt.current = Date.now();
    const result = await fetchQueryLog(contestId, Math.max(items.length, QUERY_LOG_PAGE_SIZE), 0);
    if (requestId !== requestSeq.current) return; // superseded by a newer refresh
    if (result.kind === "ok") {
      setItems(result.items);
      setTotal(result.total);
      setFailed(false);
    } else {
      // The list stays as it was, possibly a row behind, but the failure is
      // shown.
      setFailed(true);
    }
  }, [contestId, items.length]);

  // Only on the transition into being shown. Seeded from the initial
  // `active`, so opening on this tab does not refetch what page.tsx fetched.
  const wasActive = useRef(active);
  useEffect(() => {
    if (active && !wasActive.current && Date.now() - lastRefreshAt.current >= QUERY_LOG_REFRESH_MIN_INTERVAL_MS) {
      void refresh();
    }
    wasActive.current = active;
  }, [active, refresh]);

  const loadMore = async () => {
    setLoadingMore(true);
    const result = await fetchQueryLog(contestId, QUERY_LOG_PAGE_SIZE, items.length);
    setLoadingMore(false);
    if (result.kind === "ok") {
      setItems((prev) => [...prev, ...result.items]);
      setTotal(result.total);
      setFailed(false);
    } else {
      setFailed(true);
    }
  };

  const retry = async () => {
    setLoadingMore(true);
    await refresh();
    setLoadingMore(false);
  };

  if (items.length === 0) {
    return (
      <div className="flex flex-col items-start gap-2 p-4">
        <p role={failed ? "alert" : undefined} className={cn("text-body", failed ? "text-bad" : "text-ink-2")}>
          {failed ? t.failed : t.empty}
        </p>
        {failed ? (
          <button
            type="button"
            onClick={retry}
            disabled={loadingMore}
            className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
          >
            {loadingMore ? t.loadingMore : t.retry}
          </button>
        ) : null}
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-4">
      {/* A link to the whole session's CSV rather than serialising `items`,
          which hold only the loaded pages. Offered only when the log is not
          empty. */}
      <ExportMenu
        heading={t.export.heading}
        formats={[
          {
            format: "CSV",
            href: `${API_PREFIX}/contests/${contestId}/play/log.csv`,
            label: t.export.label,
          },
        ]}
        className="self-end"
      />
      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full border-collapse text-body">
          {/* Opaque, or the rows would show through the sticky head. */}
          <thead className="sticky top-0 bg-bg">
            <tr className="border-b border-edge">
              <th className="p-2 text-left font-medium text-ink">{t.columns.sql}</th>
              <th className="p-2 text-left font-medium text-ink">{t.columns.status}</th>
              <th className="p-2 text-left font-medium text-ink">{t.columns.duration}</th>
              <th className="p-2 text-left font-medium text-ink">{t.columns.rows}</th>
              <th className="p-2 text-left font-medium text-ink">{t.columns.when}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((entry) => (
              <LogRow key={entry.id} contestId={contestId} entry={entry} locale={locale} dict={dict} />
            ))}
          </tbody>
        </table>
      </div>

      {failed ? (
        <div className="flex items-center gap-3">
          <p role="alert" className="text-small text-bad">
            {t.failed}
          </p>
          <button
            type="button"
            onClick={retry}
            disabled={loadingMore}
            className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
          >
            {loadingMore ? t.loadingMore : t.retry}
          </button>
        </div>
      ) : null}

      {items.length < total ? (
        <button
          type="button"
          onClick={loadMore}
          disabled={loadingMore}
          className={cn(buttonVariants({ variant: "quiet", size: "sm" }), "self-start")}
        >
          {loadingMore ? t.loadingMore : t.loadMore}
        </button>
      ) : null}
    </div>
  );
}

/** The statement read whole from the server, for a row whose page carried only its start. */
type Whole =
  | { kind: "loading" }
  | { kind: "ok"; sql: string; truncated: boolean }
  | { kind: "refused"; code: string };

/**
 * One query, and under it, once opened, its whole statement with a Copy
 * button. A page is bounded in bytes, so a long statement arrives cut
 * (`sqlTruncated`, shown with an ellipsis); opening that row reads it whole,
 * once, from the server, which spends the play read budget like a page.
 */
function LogRow({
  contestId,
  entry,
  locale,
  dict,
}: {
  contestId: string;
  entry: QueryLogEntry;
  locale: Locale;
  dict: PlayDictionary;
}) {
  const t = dict.participant.play.workspace.log;
  const [open, setOpen] = useState(false);
  const [whole, setWhole] = useState<Whole | null>(null);
  const bodyId = useId();

  const read = async () => {
    setWhole({ kind: "loading" });
    const result = await fetchQueryLogEntry(contestId, entry.id);
    setWhole(
      result.kind === "ok" ? { kind: "ok", sql: result.entry.sql, truncated: result.entry.sqlTruncated } : result,
    );
  };

  const toggle = () => {
    setOpen(!open);
    if (!open && entry.sqlTruncated && whole === null) void read();
  };

  const shown: Whole = entry.sqlTruncated
    ? (whole ?? { kind: "loading" })
    : { kind: "ok", sql: entry.sql, truncated: false };
  const start = entry.sqlTruncated ? `${entry.sql}…` : entry.sql;

  return (
    <>
      <tr className={cn("border-b border-edge", open ? null : "last:border-b-0")}>
        <td className="p-2">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="max-w-80 min-w-0 truncate font-mono text-ink" title={start}>
              {start}
            </span>
            <button
              type="button"
              aria-expanded={open}
              aria-controls={open ? bodyId : undefined}
              onClick={toggle}
              className="ml-auto shrink-0 text-small text-ink-2 underline-offset-4 hover:text-ink hover:underline"
            >
              {open ? t.hide : t.show}
            </button>
          </div>
        </td>
        <td className="p-2">
          <StatusBadge status={entry.status} labels={t.status} />
        </td>
        <td className="p-2 font-mono text-ink-2 tabular-nums">
          {entry.durationMs !== undefined ? t.durationMs.replace("{n}", String(entry.durationMs)) : ""}
        </td>
        <td className="p-2 font-mono text-ink-2 tabular-nums">{entry.rowCount !== undefined ? entry.rowCount : ""}</td>
        <td className="p-2 text-small text-ink-2 whitespace-nowrap">{formatMoment(entry.executedAt, { locale })}</td>
      </tr>
      {open ? (
        <tr id={bodyId} className="border-b border-edge last:border-b-0">
          <td colSpan={5} className="px-2 pb-2">
            {/* `w-0 min-w-full`: the cell takes the table's width without the
                statement's longest line widening the table, so the block
                scrolls inside itself. */}
            <div role="region" aria-label={t.statement} className="flex w-0 min-w-full flex-col gap-2">
              {shown.kind === "loading" ? (
                <p role="status" className="text-small text-ink-2">
                  {t.loadingStatement}
                </p>
              ) : shown.kind === "refused" ? (
                <div className="flex flex-wrap items-center gap-3">
                  <p role="alert" className="text-small text-bad">
                    {messageForCode(shown.code, dict.errors)}
                  </p>
                  <button
                    type="button"
                    onClick={read}
                    className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
                  >
                    {t.retry}
                  </button>
                </div>
              ) : (
                <>
                  <SqlBlock sql={shown.sql} />
                  <div className="flex flex-wrap items-center gap-3">
                    <CopyButton text={shown.sql} labels={t} />
                    {shown.truncated ? <p className="text-small text-warn">{t.shortened}</p> : null}
                  </div>
                </>
              )}
            </div>
          </td>
        </tr>
      ) : null}
    </>
  );
}

/**
 * The status in the console's tones: running neutral, ok good, anything else
 * bad.
 */
function StatusBadge({ status, labels }: { status: string; labels: Record<string, string> }) {
  const label = labels[status] ?? status;
  const tone =
    status === "ok" ? "text-good" : status === "running" ? "text-ink-2" : "text-bad";
  return <span className={cn("font-mono text-small", tone)}>{label}</span>;
}

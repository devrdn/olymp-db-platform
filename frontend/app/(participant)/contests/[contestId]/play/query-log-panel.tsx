"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { ExportMenu } from "@/components/product/export-menu";
import { buttonVariants } from "@/components/ui/button";
import { API_PREFIX } from "@/lib/api/client";
import { QUERY_LOG_PAGE_SIZE, QUERY_LOG_REFRESH_MIN_INTERVAL_MS } from "@/lib/api/querylog-terms";
import type { QueryLogEntry } from "@/lib/api/querylog";
import { formatMoment } from "@/lib/format/datetime";
import type { PlayDictionary } from "./dictionary";
import type { Locale } from "@/lib/i18n/config";
import { cn } from "@/lib/utils";

import { fetchQueryLogAction } from "./actions";

/**
 * The "Query log" tab: every statement this participant has run in this
 * contest, newest first, surviving a reload.
 *
 * `initial` comes from page.tsx's server-side read; `initial.failed` marks a
 * read that failed, so the panel shows the failure with a retry instead of
 * the "no queries yet" an empty log shows.
 *
 * The panel stays mounted and refreshes on becoming `active`, not after each
 * run: its reads (`AdmitRead`) share the per-minute budget with `Run`.
 *
 * It loads `QUERY_LOG_PAGE_SIZE` rows at a time (querylog-terms.ts), so a
 * long log is not hundreds of rows re-rendered per page.
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
    const result = await fetchQueryLogAction(contestId, Math.max(items.length, QUERY_LOG_PAGE_SIZE), 0);
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
    const result = await fetchQueryLogAction(contestId, QUERY_LOG_PAGE_SIZE, items.length);
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
            {items.map((entry, i) => (
              <tr key={i} className="border-b border-edge last:border-b-0">
                {/* A page is bounded in bytes too, so a long statement arrives
                    truncated (`sqlTruncated`); the ellipsis says so, in the tooltip
                    as well. The full text is in the CSV. */}
                <td
                  className="max-w-80 truncate p-2 font-mono text-ink"
                  title={entry.sqlTruncated ? `${entry.sql}…` : entry.sql}
                >
                  {entry.sqlTruncated ? `${entry.sql}…` : entry.sql}
                </td>
                <td className="p-2">
                  <StatusBadge status={entry.status} labels={t.status} />
                </td>
                <td className="p-2 font-mono text-ink-2 tabular-nums">
                  {entry.durationMs !== undefined ? t.durationMs.replace("{n}", String(entry.durationMs)) : ""}
                </td>
                <td className="p-2 font-mono text-ink-2 tabular-nums">
                  {entry.rowCount !== undefined ? entry.rowCount : ""}
                </td>
                <td className="p-2 text-small text-ink-2 whitespace-nowrap">
                  {formatMoment(entry.executedAt, { locale })}
                </td>
              </tr>
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

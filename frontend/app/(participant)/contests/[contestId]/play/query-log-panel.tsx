"use client";

import { useEffect, useRef, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { QUERY_LOG_PAGE_SIZE } from "@/lib/api/querylog-terms";
import type { QueryLogEntry } from "@/lib/api/querylog";
import { formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Locale } from "@/lib/i18n/config";
import { cn } from "@/lib/utils";

import { fetchQueryLogAction } from "./actions";

/**
 * The "Query log" tab: every statement this participant has run in this
 * contest, newest first — what survives a reload, per the plan's own
 * requirement that a refresh mid-olympiad must not lose the history.
 *
 * `initial` is what page.tsx already fetched server-side (the same pattern
 * the story and the questions use), so the first paint needs no client round
 * trip at all. `refreshToken` changes once per completed query
 * (`ConsoleEditor`'s own `onResult`, lifted through the workspace) and is
 * what keeps the log current without the participant reloading the page —
 * on a change, this refetches exactly as many rows as are currently loaded
 * (never fewer), so an already-expanded "load more" view does not appear to
 * shrink back to one page the moment a fresh query lands.
 *
 * Bounded, deliberately: QUERY_LOG_PAGE_SIZE (50) is what loads at a time,
 * and the table only ever grows by that much per "load more" press — see
 * querylog-terms.ts's own doc for why fifty. A contest can run two hours, and
 * a participant who never stops querying can put hundreds of rows in their
 * own log; this is what keeps that from being hundreds of table rows
 * re-rendered on every keystroke of an unrelated query.
 */
export function QueryLogPanel({
  contestId,
  initial,
  refreshToken,
  locale,
  dict,
}: {
  contestId: string;
  initial: { items: QueryLogEntry[]; total: number };
  /** Bumped once per completed query — see this component's own doc. */
  refreshToken: number;
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.participant.play.workspace.log;
  const [items, setItems] = useState(initial.items);
  const [total, setTotal] = useState(initial.total);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failed, setFailed] = useState(false);

  // Skips the refresh on the very first render: `initial` already is the
  // freshest read as of when the page loaded, and refetching it again the
  // instant this mounts would be a wasted round trip for data already in
  // hand.
  const mounted = useRef(false);
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true;
      return;
    }
    let cancelled = false;
    void fetchQueryLogAction(contestId, Math.max(items.length, QUERY_LOG_PAGE_SIZE), 0).then((result) => {
      if (cancelled) return;
      if (result.kind === "ok") {
        setItems(result.items);
        setTotal(result.total);
        setFailed(false);
      }
      // A refresh that fails leaves the list exactly as it was — the
      // participant's own history a moment ago is still true, just possibly
      // one row behind, and nothing here is worth interrupting them over.
    });
    return () => {
      cancelled = true;
    };
    // items.length is read once, at the moment refreshToken changes, to
    // decide how large a page to ask for — not a dependency this effect
    // should re-run for on its own, or every setItems call above would
    // trigger it again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshToken, contestId]);

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

  if (items.length === 0) {
    return <p className="p-4 text-body text-ink-2">{t.empty}</p>;
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-4">
      <div className="min-h-0 flex-1 overflow-auto border border-edge">
        <table className="w-full border-collapse text-body">
          <thead className="sticky top-0 bg-surface">
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
                <td className="max-w-80 truncate p-2 font-mono text-ink" title={entry.sql}>
                  {entry.sql}
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

      {failed ? <p role="alert" className="text-small text-bad">{t.failed}</p> : null}

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

/** The status column's own colour, echoing the console's own passing/failing
 * distinction: running is neutral, ok is fine, everything else is worth a
 * second look. */
function StatusBadge({ status, labels }: { status: string; labels: Record<string, string> }) {
  const label = labels[status] ?? status;
  const tone =
    status === "ok" ? "text-good" : status === "running" ? "text-ink-2" : "text-bad";
  return <span className={cn("font-mono text-small", tone)}>{label}</span>;
}

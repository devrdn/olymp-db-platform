"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { QUERY_LOG_PAGE_SIZE, QUERY_LOG_REFRESH_MIN_INTERVAL_MS } from "@/lib/api/querylog-terms";
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
 * trip at all. `initial.failed` is true when that server-side read itself
 * failed and page.tsx degraded to an empty page rather than losing the whole
 * screen over it (finding 4): without this flag, a broken log and a
 * genuinely empty one rendered as the identical "you have not run a query
 * yet", and a participant trying to recall what they already tried had no
 * way to tell a real answer from a shrug — and no retry, since `total` being
 * zero hides "load more" too. This is what lets the panel show the
 * dictionary's own failure string instead, with a button to try again.
 *
 * `active` is whether the "Query log" tab is the one currently showing
 * (finding 3). This panel stays mounted the whole time — never unmounted by
 * a tab switch, `TabsContent`'s own doc — so it refreshes itself on the
 * transition into being shown rather than once per completed query, which is
 * what an earlier version of this component did. That cost more than it
 * looked like: `AdmitRead` (what this refetch calls) shares its per-minute
 * budget with `Run`, so every completed query was quietly spending a second
 * unit of the participant's own rate limit — and spending it on a tab that,
 * because a completed run switches the workspace straight to "Result", was
 * essentially never even the one showing when the refetch fired. Refreshing
 * on entry instead costs one request per deliberate visit to this tab, which
 * is also the one moment stale data would actually be seen.
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
  active,
  locale,
  dict,
}: {
  contestId: string;
  initial: { items: QueryLogEntry[]; total: number; failed: boolean };
  /** Whether the "Query log" tab is the one currently showing — see this component's own doc. */
  active: boolean;
  locale: Locale;
  dict: Dictionary;
}) {
  const t = dict.participant.play.workspace.log;
  const [items, setItems] = useState(initial.items);
  const [total, setTotal] = useState(initial.total);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failed, setFailed] = useState(initial.failed);

  // Finding 4 of the follow-up review: two problems with refreshing on every
  // transition into this tab. First, nothing stopped a superseded response
  // from overwriting a newer one — the `cancelled` flag the very first
  // version of this effect used (still visible in this file's own history)
  // was dropped when this became a plain `async` callback, and two refreshes
  // really can overlap: a slow one from an earlier transition still in
  // flight when a later transition starts another. `requestSeq` is a ticket
  // number bumped at the start of every refresh; a response is only applied
  // if its ticket is still the most recent one issued, so an answer that
  // arrives late can no longer clobber one that already landed. Second, a
  // student who idly toggles Result and Log spent one `AdmitRead` — the
  // budget `Run` itself shares — on every single transition, even back into
  // data that was just fetched a moment ago; `lastRefreshAt` is when a
  // refresh last actually ran, and the effect below skips firing another one
  // inside `QUERY_LOG_REFRESH_MIN_INTERVAL_MS` of it. That gate applies only
  // to the automatic, on-transition refresh: `retry` (an explicit click,
  // always after a failure) calls this same function and always goes
  // through, which is what a student pressing "retry" actually asked for.
  //
  // How large a page to ask for depends on how many rows are already
  // loaded, so `refresh` closes over `items.length` directly rather than
  // over a ref holding it: reading a ref during render to avoid this
  // dependency is exactly what `react-hooks/refs` refuses (a render is not
  // guaranteed to commit), and there is no render-time read to avoid here in
  // the first place — `items.length` already is a render-time value.
  // `refresh` getting a new identity whenever the list changes costs
  // nothing: the effect below only ever acts on it through the
  // active-transition guard, so a changed identity with no real transition
  // re-runs the effect but calls nothing.
  const requestSeq = useRef(0);
  const lastRefreshAt = useRef(0);
  const refresh = useCallback(async () => {
    const requestId = ++requestSeq.current;
    lastRefreshAt.current = Date.now();
    const result = await fetchQueryLogAction(contestId, Math.max(items.length, QUERY_LOG_PAGE_SIZE), 0);
    if (requestId !== requestSeq.current) return; // a newer refresh has already started; this answer is stale
    if (result.kind === "ok") {
      setItems(result.items);
      setTotal(result.total);
      setFailed(false);
    } else {
      // A refresh that fails leaves the list exactly as it was — the
      // participant's own history a moment ago is still true, just possibly
      // one row behind — but says so rather than pretending nothing is
      // wrong (finding 4).
      setFailed(true);
    }
  }, [contestId, items.length]);

  // Fires only on the transition into this tab being shown — see this
  // component's own doc (finding 3) for why not on every completed run.
  // Seeded from the initial `active` value so a page that opens straight on
  // this tab does not immediately refetch the same data page.tsx just
  // fetched server-side.
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

/** The status column's own colour, echoing the console's own passing/failing
 * distinction: running is neutral, ok is fine, everything else is worth a
 * second look. */
function StatusBadge({ status, labels }: { status: string; labels: Record<string, string> }) {
  const label = labels[status] ?? status;
  const tone =
    status === "ok" ? "text-good" : status === "running" ? "text-ink-2" : "text-bad";
  return <span className={cn("font-mono text-small", tone)}>{label}</span>;
}

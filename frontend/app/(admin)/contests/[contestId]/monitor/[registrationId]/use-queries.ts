"use client";

import { useMemo } from "react";

import { useQueryLog, type QueryLogRefresh, type QueryOutcome } from "@/components/product/use-query-log";
import { fetchQueries, fetchTimeline, MAX_FEED_PAGE, type QueriesPage } from "@/lib/api/monitor";

import { MAX_RUNNING_TRIES } from "../feed-list";
import { MONITOR_POLL_MS } from "../use-monitor";

export { SEARCH_DEBOUNCE_MS } from "@/components/product/use-query-log";

/**
 * The queries tab's list: one participant's queries, as the monitoring routes
 * serve them.
 *
 * What the list does is `useQueryLog`, which the participant's own report uses
 * too. This is the monitoring half of it: the route the pages come from, and
 * the one thing the staff screen can do that the report cannot — find out how
 * a query that was still running ended. A query is journalled as `running`
 * before it runs and the queries route is keyset by position, so it would not
 * deliver it again; the timeline is asked for that stretch instead.
 */
export function useQueries({
  contestId,
  registrationId,
  initial,
}: {
  contestId: string;
  registrationId: string;
  initial: QueriesPage;
}) {
  const refresh = useMemo<QueryLogRefresh>(
    () => ({
      everyMs: MONITOR_POLL_MS,
      tries: MAX_RUNNING_TRIES,
      async outcomes(running, options) {
        let from = running[0].executedAt;
        let until = from;
        for (const q of running) {
          if (q.executedAt < from) from = q.executedAt;
          if (q.executedAt > until) until = q.executedAt;
        }
        const page = await fetchTimeline(
          contestId,
          registrationId,
          // `from` inclusive, `until` exclusive, and times to the millisecond.
          { kinds: ["query"], from, until: new Date(Date.parse(until) + 1).toISOString(), limit: MAX_FEED_PAGE },
          options,
        );
        const ended: QueryOutcome[] = [];
        for (const item of page.items) {
          const d = item.detail;
          if (d.type !== "query") continue;
          ended.push({ id: d.id, status: d.status, error: d.error, durationMs: d.durationMs, rowCount: d.rowCount });
        }
        return ended;
      },
    }),
    [contestId, registrationId],
  );

  return useQueryLog({
    initial,
    page: (params, options) => fetchQueries(contestId, registrationId, params, options),
    refresh,
  });
}

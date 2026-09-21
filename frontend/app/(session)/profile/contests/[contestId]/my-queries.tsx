"use client";

import { QueryLog } from "@/components/product/query-log";
import { useQueryLog } from "@/components/product/use-query-log";
import type { QueriesPage } from "@/lib/api/journal";
import { fetchMyQueries } from "@/lib/api/profile";

import type { ReportDict } from "./report-tabs";

/**
 * Every statement this participant ran in this contest, whole, newest first,
 * with the same filter, the same search and the same paging the organiser's
 * screen has — and without the address column.
 *
 * No refresh of running queries: the report opens only once the contest has
 * ended for its reader, so nothing in this list can still be running. The
 * monitoring screen passes one because it reads a contest that may still be
 * going on.
 */
export function MyQueries({
  contestId,
  initial,
  t,
  statuses,
  locale,
}: {
  contestId: string;
  initial: QueriesPage;
  t: ReportDict;
  /**
   * Each query status in words. The participant's own, from the play
   * screen's log: this is the same person reading the same statuses, and a
   * second wording of "timed out" for the same audience is how two screens
   * come to disagree about what happened.
   */
  statuses: Record<string, string>;
  locale: string;
}) {
  const log = useQueryLog({
    initial,
    page: (params, options) => fetchMyQueries(contestId, params, options),
  });

  return (
    <QueryLog
      log={log}
      labels={t.queries}
      statuses={statuses}
      problems={t.problems}
      listLabel={t.tabs.queries}
      locale={locale}
    />
  );
}

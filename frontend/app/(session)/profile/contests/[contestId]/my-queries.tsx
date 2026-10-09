"use client";

import { QueryLog } from "@/components/product/query-log";
import { useQueryLog } from "@/components/product/use-query-log";
import type { QueriesPage } from "@/lib/api/journal";
import { fetchMyQueries } from "@/lib/api/profile";

import type { ReportDict } from "./report-tabs";

/**
 * The participant's statements with the organiser screen's filter, search and
 * paging, minus the address column. No running-query refresh: the report opens
 * only after the contest ended for its reader.
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
  /** Status wording from the play screen's log, so the same reader never sees two wordings. */
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

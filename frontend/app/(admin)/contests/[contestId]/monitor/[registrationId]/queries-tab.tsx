"use client";

import { QueryLog } from "@/components/product/query-log";
import type { QueriesPage } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { useQueries } from "./use-queries";

/**
 * The SQL queries tab (SPEC.md §5.1): `QueryLog` fed by `useQueries`, with staff
 * wording and the address column the participant's copy lacks.
 */
export function QueriesTab({
  contestId,
  registrationId,
  initial,
  dict,
  locale,
}: {
  contestId: string;
  registrationId: string;
  initial: QueriesPage;
  dict: Dictionary;
  locale: string;
}) {
  const queries = useQueries({ contestId, registrationId, initial });

  return (
    <QueryLog
      log={queries}
      labels={dict.workspace.monitor.participant.queries}
      statuses={dict.workspace.monitor.feed.queryStatus}
      problems={dict.workspace.monitor.problems}
      listLabel={dict.workspace.monitor.participant.tabs.queries}
      locale={locale}
      address
    />
  );
}

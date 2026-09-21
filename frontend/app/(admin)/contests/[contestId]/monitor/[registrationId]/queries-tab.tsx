"use client";

import { QueryLog } from "@/components/product/query-log";
import type { QueriesPage } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { useQueries } from "./use-queries";

/**
 * The SQL queries tab (design §6): every statement the participant ran,
 * whole, newest first, with a status filter and a text search, fifty at a
 * time.
 *
 * The panel is `QueryLog`, which the participant's own report shows too; this
 * is the monitoring half — the route the pages come from (`useQueries`), the
 * organiser's words, and the address column, which is the one column a
 * participant's own copy of this list does not carry.
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

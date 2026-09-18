"use client";

import { useId } from "react";

import { buttonVariants } from "@/components/ui/button";
import { MAX_QUERY_SEARCH, QUERY_STATUSES, type QueriesPage } from "@/lib/api/monitor";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { Problem } from "../monitor-view";
import { QueryRow } from "./query-row";
import { useQueries } from "./use-queries";

const CONTROL = "h-(--control-h) w-full min-w-0 border border-edge bg-bg px-2.5 text-control text-ink";

/**
 * The SQL queries tab (design §6): every statement the participant ran,
 * whole, newest first, with a status filter and a text search, fifty at a
 * time. What the list does lives in `use-queries.ts`; each line is a
 * `QueryRow`.
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
  const t = dict.workspace.monitor.participant.queries;
  const statuses = dict.workspace.monitor.feed.queryStatus as Record<string, string>;
  const queries = useQueries({ contestId, registrationId, initial });
  const ids = useId();
  const filtered = queries.status !== "" || queries.search !== "";

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
        <div className="flex min-w-0 grow basis-40 flex-col gap-1.5 sm:max-w-56">
          <label htmlFor={`${ids}-status`} className="font-mono text-label text-ink-3 uppercase">
            {t.status}
          </label>
          <select
            id={`${ids}-status`}
            value={queries.status}
            onChange={(event) => void queries.setStatus(event.target.value)}
            className={CONTROL}
          >
            <option value="">{t.anyStatus}</option>
            {QUERY_STATUSES.map((status) => (
              <option key={status} value={status}>
                {statuses[status] ?? status}
              </option>
            ))}
          </select>
        </div>
        <div className="flex min-w-0 grow basis-60 flex-col gap-1.5">
          <label htmlFor={`${ids}-search`} className="font-mono text-label text-ink-3 uppercase">
            {t.search}
          </label>
          <input
            id={`${ids}-search`}
            type="search"
            value={queries.search}
            maxLength={MAX_QUERY_SEARCH}
            placeholder={t.searchPlaceholder}
            aria-describedby={`${ids}-search-note`}
            onChange={(event) => queries.setSearch(event.target.value)}
            className={cn(CONTROL, "font-mono placeholder:font-sans placeholder:text-ink-3")}
          />
        </div>
      </div>
      <p id={`${ids}-search-note`} className="-mt-2 text-small text-ink-3 empty:hidden">
        {queries.search.length >= MAX_QUERY_SEARCH ? t.searchTooLong.replace("{n}", String(MAX_QUERY_SEARCH)) : ""}
      </p>

      <Problem problem={queries.problem} t={dict.workspace.monitor} className="empty:-mt-4" />

      {queries.items.length === 0 ? (
        <p className="text-body text-ink-2" aria-busy={queries.loading}>
          {filtered ? t.noMatch : t.empty}
        </p>
      ) : (
        <ol
          aria-label={dict.workspace.monitor.participant.tabs.queries}
          aria-busy={queries.loading}
          className="flex min-w-0 flex-col border-t border-line"
        >
          {queries.items.map((query) => (
            <QueryRow key={query.cursor} query={query} dict={dict} locale={locale} />
          ))}
        </ol>
      )}

      {queries.more && queries.items.length > 0 ? (
        <button
          type="button"
          onClick={() => void queries.loadMore()}
          disabled={queries.loadingMore}
          className={cn(buttonVariants({ variant: "secondary", size: "sm" }), "self-start")}
        >
          {queries.loadingMore ? t.loading : t.loadMore}
        </button>
      ) : null}
    </div>
  );
}

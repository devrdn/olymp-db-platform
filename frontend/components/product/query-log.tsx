"use client";

import { useId } from "react";

import { buttonVariants } from "@/components/ui/button";
import { MAX_QUERY_SEARCH, QUERY_STATUSES } from "@/lib/api/journal";
import { cn } from "@/lib/utils";

import { QueryRow, type QueryRowLabels } from "./query-row";
import type { QueryLogState } from "./use-query-log";

const CONTROL = "h-(--control-h) w-full min-w-0 border border-edge bg-bg px-2.5 text-control text-ink";

export type QueryLogLabels = QueryRowLabels & {
  status: string;
  anyStatus: string;
  search: string;
  searchPlaceholder: string;
  searchTooLong: string;
  empty: string;
  noMatch: string;
  loadMore: string;
  loading: string;
};

export type QueryLogProblems = { forbidden: string; tooOften: string; failed: string };

/**
 * Every statement someone ran, newest first, with a status filter and search,
 * fifty at a time. Shared by the staff monitoring page and the participant's
 * report, which differ only in props; the caller holds `useQueryLog` because it
 * knows the route.
 */
export function QueryLog({
  log,
  labels,
  statuses,
  problems,
  listLabel,
  locale,
  address = false,
}: {
  log: QueryLogState;
  labels: QueryLogLabels;
  statuses: Record<string, string>;
  problems: QueryLogProblems;
  /** Names the list for a screen reader. */
  listLabel: string;
  locale: string;
  address?: boolean;
}) {
  const ids = useId();
  const filtered = log.status !== "" || log.search !== "";
  const problem = !log.problem
    ? ""
    : log.problem.kind === "forbidden"
      ? problems.forbidden
      : log.problem.kind === "tooOften"
        ? problems.tooOften.replace("{seconds}", String(log.problem.seconds))
        : problems.failed;

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
        <div className="flex min-w-0 grow basis-40 flex-col gap-1.5 sm:max-w-56">
          <label htmlFor={`${ids}-status`} className="font-mono text-label text-ink-3 uppercase">
            {labels.status}
          </label>
          <select
            id={`${ids}-status`}
            value={log.status}
            onChange={(event) => void log.setStatus(event.target.value)}
            className={CONTROL}
          >
            <option value="">{labels.anyStatus}</option>
            {QUERY_STATUSES.map((status) => (
              <option key={status} value={status}>
                {statuses[status] ?? status}
              </option>
            ))}
          </select>
        </div>
        <div className="flex min-w-0 grow basis-60 flex-col gap-1.5">
          <label htmlFor={`${ids}-search`} className="font-mono text-label text-ink-3 uppercase">
            {labels.search}
          </label>
          <input
            id={`${ids}-search`}
            type="search"
            value={log.search}
            maxLength={MAX_QUERY_SEARCH}
            placeholder={labels.searchPlaceholder}
            aria-describedby={`${ids}-search-note`}
            onChange={(event) => log.setSearch(event.target.value)}
            className={cn(CONTROL, "font-mono placeholder:font-sans placeholder:text-ink-3")}
          />
        </div>
      </div>
      <p id={`${ids}-search-note`} className="-mt-2 text-small text-ink-3 empty:hidden">
        {log.search.length >= MAX_QUERY_SEARCH ? labels.searchTooLong.replace("{n}", String(MAX_QUERY_SEARCH)) : ""}
      </p>

      {/* Collapsed when empty rather than hidden, so the live region stays in
         the accessibility tree. */}
      <p role="status" className="text-small text-warn empty:-mt-4">
        {problem}
      </p>

      {log.items.length === 0 ? (
        <p className="text-body text-ink-2" aria-busy={log.loading}>
          {filtered ? labels.noMatch : labels.empty}
        </p>
      ) : (
        <ol aria-label={listLabel} aria-busy={log.loading} className="flex min-w-0 flex-col border-t border-line">
          {log.items.map((query) => (
            <QueryRow
              key={query.cursor}
              query={query}
              labels={labels}
              statuses={statuses}
              locale={locale}
              address={address}
            />
          ))}
        </ol>
      )}

      {log.more && log.items.length > 0 ? (
        <button
          type="button"
          onClick={() => void log.loadMore()}
          disabled={log.loadingMore}
          className={cn(buttonVariants({ variant: "secondary", size: "sm" }), "self-start")}
        >
          {log.loadingMore ? labels.loading : labels.loadMore}
        </button>
      ) : null}
    </div>
  );
}

"use client";

import { memo, useId, useMemo, useState } from "react";

import Link from "next/link";

import { Checkbox } from "@/components/ui/checkbox";
import { Tooltip } from "@/components/ui/tooltip";
import { MONITOR_FLAGS, type RosterRow } from "@/lib/api/monitor";
import { REGISTRATION_STATUSES } from "@/lib/api/people";
import { readableDuration } from "@/lib/format/bytes";
import { formatMoment, formatTime } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { COUNTER_KEYS, filterRows, sortRows, type CounterKey, type RosterFilter, type SortKey, type SortOrder } from "./roster";

type MonitorDict = Dictionary["workspace"]["monitor"];

/** The columns in the order they stand, and which way a first press sorts each. */
const COLUMNS: { key: SortKey; numeric: boolean }[] = [
  { key: "name", numeric: false },
  { key: "status", numeric: false },
  { key: "flags", numeric: true },
  ...COUNTER_KEYS.map((key) => ({ key, numeric: true })),
  { key: "lastActivity", numeric: true },
];

const CONTROL = "h-(--control-h) border border-edge bg-bg px-2.5 text-control text-ink";

/**
 * Every participant with their counters and flags (design §4, §6).
 *
 * The table is wider than the column it stands in once the feed is beside
 * it, so it scrolls sideways inside its own box — the page never does — with
 * the name held at the left edge, and down inside a bounded box with its
 * heading held at the top, so the feed beside it stays in view.
 *
 * Rows are memoised on the row object, and the rows arrive merged
 * (`mergeRoster`): a poll that changed one participant renders one row.
 */
export function ParticipantsTable({
  contestId,
  rows,
  truncated,
  fresh,
  dict,
  locale,
}: {
  contestId: string;
  rows: RosterRow[];
  truncated: boolean;
  /** Participants with a new feed item a moment ago; their rows are lit. */
  fresh: ReadonlySet<string>;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor;
  const statuses = dict.workspace.people.registration as Record<string, string>;
  const [order, setOrder] = useState<SortOrder>({ key: "name", dir: "asc" });
  const [filter, setFilter] = useState<RosterFilter>({ flaggedOnly: false, status: "", search: "" });
  const ids = useId();

  const shown = useMemo(() => sortRows(filterRows(rows, filter), order), [rows, filter, order]);

  const sortBy = (key: SortKey, numeric: boolean) =>
    setOrder((current) =>
      current.key === key
        ? { key, dir: current.dir === "asc" ? "desc" : "asc" }
        : { key, dir: numeric ? "desc" : "asc" },
    );

  const label = (key: SortKey) => t.table.columns[key];
  const filtered = filter.flaggedOnly || filter.status !== "" || filter.search.trim() !== "";

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
        <div className="flex min-w-0 grow basis-48 flex-col gap-1.5">
          <label htmlFor={`${ids}-search`} className="font-mono text-label text-ink-3 uppercase">
            {t.table.filters.search}
          </label>
          <input
            id={`${ids}-search`}
            type="search"
            value={filter.search}
            placeholder={t.table.filters.searchPlaceholder}
            onChange={(event) => setFilter((current) => ({ ...current, search: event.target.value }))}
            className={cn(CONTROL, "w-full min-w-0 placeholder:text-ink-3")}
          />
        </div>
        <div className="flex min-w-0 grow basis-40 flex-col gap-1.5">
          <label htmlFor={`${ids}-status`} className="font-mono text-label text-ink-3 uppercase">
            {t.table.filters.status}
          </label>
          <select
            id={`${ids}-status`}
            value={filter.status}
            onChange={(event) => setFilter((current) => ({ ...current, status: event.target.value }))}
            className={cn(CONTROL, "w-full")}
          >
            <option value="">{t.table.filters.anyStatus}</option>
            {REGISTRATION_STATUSES.map((status) => (
              <option key={status} value={status}>
                {statuses[status] ?? status}
              </option>
            ))}
          </select>
        </div>
        <label className="flex h-(--control-h) items-center gap-2 text-control text-ink">
          <Checkbox
            checked={filter.flaggedOnly}
            onCheckedChange={(checked) => setFilter((current) => ({ ...current, flaggedOnly: checked === true }))}
          />
          {t.table.filters.flaggedOnly}
        </label>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="font-mono text-data text-ink-3">
          {t.table.count.replace("{shown}", String(shown.length)).replace("{total}", String(rows.length))}
        </p>
        {/* Above the table rather than in the flags heading: the heading
            sits inside the table's own scroll box, which would clip the
            explanation at its edge. */}
        <span className="inline-flex items-center gap-1 text-small text-ink-3">
          <span aria-hidden>{t.flags.help}</span>
          <FlagsHelp t={t} />
        </span>
        {truncated ? (
          <p className="text-small text-warn">{t.table.truncated.replace("{n}", String(rows.length))}</p>
        ) : null}
      </div>

      {rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.table.empty}</p>
      ) : shown.length === 0 && filtered ? (
        <p className="text-body text-ink-2">{t.table.noMatch}</p>
      ) : (
        <div className="max-h-[42rem] overflow-auto border-y border-line max-narrow:max-h-[70vh]">
          {/* Separate borders, not collapsed: a collapsed border belongs to the
              table rather than the cell, so it would scroll away from under
              the held heading and name column. */}
          <table className="w-max min-w-full border-separate text-left" style={{ borderSpacing: 0 }}>
            <caption className="sr-only">{t.table.heading}</caption>
            <thead>
              <tr>
                {COLUMNS.map(({ key, numeric }, index) => {
                  const sorted = order.key === key;
                  return (
                    <th
                      key={key}
                      scope="col"
                      aria-sort={sorted ? (order.dir === "asc" ? "ascending" : "descending") : undefined}
                      className={cn(
                        "sticky top-0 z-1 border-b border-line-2 bg-bg px-2 py-2 align-bottom font-mono text-label font-medium text-ink-3 uppercase",
                        numeric && key !== "flags" && "text-right",
                        // The corner: held on both axes, above both the
                        // heading row and the name column.
                        index === 0 && "left-0 z-2",
                      )}
                    >
                      <span className={cn("inline-flex items-center", numeric && key !== "flags" && "justify-end")}>
                        <button
                          type="button"
                          onClick={() => sortBy(key, numeric)}
                          aria-label={label(key)}
                          title={t.table.sortBy.replace("{column}", label(key))}
                          className={cn(
                            "max-w-24 text-left uppercase transition-colors duration-(--t-input) ease-standard hover:text-ink",
                            sorted && "text-ink",
                          )}
                        >
                          {label(key)}
                          {sorted ? <span aria-hidden>{order.dir === "asc" ? " ↑" : " ↓"}</span> : null}
                        </button>
                      </span>
                    </th>
                  );
                })}
              </tr>
            </thead>
            <tbody>
              {shown.map((row) => (
                <ParticipantRow
                  key={row.registrationId}
                  row={row}
                  fresh={fresh.has(row.registrationId)}
                  contestId={contestId}
                  t={t}
                  statuses={statuses}
                  locale={locale}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function FlagsHelp({ t }: { t: MonitorDict }) {
  return (
    <Tooltip label={t.flags.help}>
      <span className="block">{t.flags.notProof}</span>
      <span className="mt-2 flex flex-col gap-1.5">
        {MONITOR_FLAGS.map((flag) => (
          <span key={flag} className="block">
            {t.flags[flag].explain}
          </span>
        ))}
      </span>
    </Tooltip>
  );
}

const CELL = "border-b border-line px-2 py-1.5";
const NUMBER = "text-right font-mono text-data tabular-nums";

/**
 * One participant. Memoised: the props are the row object, which survives a
 * poll that did not change it, and values that do not change between polls.
 */
const ParticipantRow = memo(function ParticipantRow({
  row,
  fresh,
  contestId,
  t,
  statuses,
  locale,
}: {
  row: RosterRow;
  fresh: boolean;
  contestId: string;
  t: MonitorDict;
  statuses: Record<string, string>;
  locale: string;
}) {
  const count = (key: CounterKey) => (
    <td key={key} className={cn(CELL, NUMBER, row[key] === 0 ? "text-ink-3" : "text-ink")}>
      {key === "awayMs" ? readableDuration(row.awayMs / 1000) : row[key]}
    </td>
  );

  return (
    <tr data-fresh={fresh || undefined} className="group">
      <td
        className={cn(
          CELL,
          // Held at the left edge while the counters scroll under it; opaque,
          // or the numbers would scroll through the name.
          "sticky left-0 z-1 max-w-56 min-w-40 bg-bg",
          "transition-colors duration-(--t-state) ease-standard group-data-fresh:bg-accent-wash",
        )}
      >
        <Link
          href={`/contests/${contestId}/monitor/${row.registrationId}`}
          className="block truncate text-control text-ink underline-offset-4 hover:underline"
        >
          {row.fullName || row.login}
        </Link>
        <span className="block truncate font-mono text-label text-ink-3">{row.login}</span>
      </td>
      <td className={cn(CELL, "text-small whitespace-nowrap text-ink-2")}>{statuses[row.status] ?? row.status}</td>
      <td className={cn(CELL, "min-w-28")}>
        <span className="flex flex-wrap gap-1">
          {MONITOR_FLAGS.filter((flag) => row.flags[flag]).map((flag) => (
            <span
              key={flag}
              title={t.flags[flag].explain}
              className="rounded-full bg-warn-wash px-1.5 font-mono text-label whitespace-nowrap text-warn uppercase"
            >
              {t.flags[flag].label}
            </span>
          ))}
        </span>
      </td>
      {COUNTER_KEYS.map(count)}
      <td className={cn(CELL, NUMBER, "whitespace-nowrap text-ink-2")}>
        {row.lastActivity ? (
          <time dateTime={row.lastActivity} title={formatMoment(row.lastActivity, { locale })}>
            {formatTime(row.lastActivity, { locale })}
          </time>
        ) : (
          <span className="text-ink-3">{t.table.never}</span>
        )}
      </td>
    </tr>
  );
});

"use client";

import { memo, useCallback, useDeferredValue, useId, useLayoutEffect, useMemo, useRef, useState } from "react";

import { hasPrize, PlaceBadge, PrizeBadge } from "@/components/product/standings";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import type { Scoring } from "@/lib/api/contests";
import type { ResultsRow } from "@/lib/api/results";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  allSelection,
  matchesSearch,
  parsePlace,
  prizeSelection,
  rangeSelection,
  toggled,
} from "./selection";

/**
 * Who a results export carries: a place range, quick picks, a search and a
 * checkbox per row. The places are the export's own, counted without the
 * disqualified, so "from 1 to 10" ticks the rows the file calls 1 to 10.
 */
export function ParticipantList({
  rows,
  scoring,
  selected,
  onChange,
  dict,
}: {
  rows: ResultsRow[];
  scoring: Scoring;
  selected: ReadonlySet<string>;
  onChange(next: Set<string>): void;
  dict: Dictionary;
}) {
  const t = dict.leaderboard.export;
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [query, setQuery] = useState("");
  // Two thousand rows are filtered behind the keystroke, not in front of it.
  const search = useDeferredValue(query);
  const visible = useMemo(() => rows.filter((row) => matchesSearch(row, search)), [rows, search]);
  const prizes = hasPrize(rows);
  const icpc = scoring === "icpc";
  const ids = useId();

  // A row's toggle must not change with the selection, or every memoized row
  // would re-render on every tick.
  const latest = useRef({ selected, onChange });
  useLayoutEffect(() => {
    latest.current = { selected, onChange };
  });
  const toggle = useCallback((id: string) => {
    latest.current.onChange(toggled(latest.current.selected, id));
  }, []);

  const rangeTyped = (from.trim() === "" || parsePlace(from) !== null) && (to.trim() === "" || parsePlace(to) !== null);

  return (
    <div className="@container flex min-w-0 flex-col gap-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h3 className="font-mono text-label text-ink-3 uppercase">{t.whom}</h3>
        <p className="font-mono text-data text-ink-3" aria-live="polite">
          {t.ticked.replace("{selected}", String(selected.size)).replace("{total}", String(rows.length))}
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2 text-small text-ink-2">
        <label htmlFor={`${ids}-from`}>{t.from}</label>
        <Input
          id={`${ids}-from`}
          inputMode="numeric"
          autoComplete="off"
          value={from}
          onChange={(event) => setFrom(event.target.value)}
          className="h-7 w-14 px-2 font-mono text-data"
        />
        <label htmlFor={`${ids}-to`}>{t.to}</label>
        <Input
          id={`${ids}-to`}
          inputMode="numeric"
          autoComplete="off"
          value={to}
          onChange={(event) => setTo(event.target.value)}
          className="h-7 w-14 px-2 font-mono text-data"
        />
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={!rangeTyped}
          onClick={() => onChange(rangeSelection(rows, parsePlace(from), parsePlace(to)))}
        >
          {t.tick}
        </Button>
        <span aria-hidden className="mx-1 h-5 w-px bg-line-2" />
        <Button type="button" variant="quiet" size="sm" onClick={() => onChange(prizeSelection(rows))}>
          {t.prizeWinners}
        </Button>
        <Button type="button" variant="quiet" size="sm" onClick={() => onChange(allSelection(rows))}>
          {t.all}
        </Button>
        <Button type="button" variant="quiet" size="sm" onClick={() => onChange(new Set())}>
          {t.none}
        </Button>
      </div>

      <Input
        type="search"
        aria-label={t.search}
        placeholder={t.search}
        autoComplete="off"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />

      {rows.length === 0 ? (
        <p className="text-body text-ink-2">{t.empty}</p>
      ) : (
        <div className="flex flex-col">
          <Columns icpc={icpc} prizes={prizes} dict={dict} />
          {visible.length === 0 ? (
            <p className="py-3 text-small text-ink-3">{t.noMatch}</p>
          ) : (
            <ul>
              {visible.map((row) => (
                <ParticipantRow
                  key={row.id}
                  row={row}
                  checked={selected.has(row.id)}
                  onToggle={toggle}
                  icpc={icpc}
                  prizes={prizes}
                  dict={dict}
                />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * The cells of a row after its checkbox, place and medal. In a list 28rem
 * wide or more the two numbers are columns of their own; in a narrower one (a
 * phone, or the column beside the contest's sections on a small laptop) they
 * share the login's line, so the name keeps the whole first line. The list's
 * own width decides, not the viewport's.
 */
const WHO = "grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)_3.5rem_4.5rem] items-center gap-x-3 @max-md:grid-cols-[minmax(0,1fr)_auto]";
const NUMBERS = "contents @max-md:col-start-2 @max-md:row-start-2 @max-md:flex @max-md:items-baseline @max-md:gap-1.5";
const SOLVED = "col-start-2 row-span-2 row-start-1 text-right";
const SCORE = "col-start-3 row-span-2 row-start-1 text-right";

/** The column names, laid out as a row is; a reader hears each number's name from the row itself. */
function Columns({ icpc, prizes, dict }: { icpc: boolean; prizes: boolean; dict: Dictionary }) {
  const c = dict.leaderboard.columns;
  return (
    <div
      aria-hidden
      className="flex items-center gap-3 border-b border-line-2 py-2 font-mono text-label text-ink-3 uppercase @max-md:gap-2"
    >
      <span className="size-4 shrink-0" />
      <span className="w-12 shrink-0 truncate @max-md:invisible @max-md:w-7">{c.place}</span>
      {prizes ? <span className="w-7 shrink-0" /> : null}
      <span className={WHO}>
        <span className="col-start-1 row-start-1 truncate">{c.participant}</span>
        <span className={cn(NUMBERS, "@max-md:row-start-1")}>
          <span className={cn(SOLVED, "truncate")}>{c.solved}</span>
          <span className="@md:hidden">·</span>
          <span className={cn(SCORE, "truncate")}>{icpc ? c.penalty : c.points}</span>
        </span>
      </span>
    </div>
  );
}

const ParticipantRow = memo(function ParticipantRow({
  row,
  checked,
  onToggle,
  icpc,
  prizes,
  dict,
}: {
  row: ResultsRow;
  checked: boolean;
  onToggle(id: string): void;
  icpc: boolean;
  prizes: boolean;
  dict: Dictionary;
}) {
  const l = dict.leaderboard;
  const name = row.deleted ? l.deleted : row.fullName || row.login || row.email || l.deleted;
  const score = icpc ? row.penalty : row.points;
  const nameId = useId();

  return (
    // Off screen, a row costs no layout: two thousand of them stay cheap.
    <li className="border-b border-line [contain-intrinsic-size:auto_4.3125rem] [content-visibility:auto]">
      <label
        className={cn(
          "flex cursor-pointer items-center gap-3 py-2 @max-md:gap-2",
          row.disqualified && "text-ink-3",
        )}
      >
        <Checkbox checked={checked} onCheckedChange={() => onToggle(row.id)} aria-labelledby={nameId} />
        <span className="w-12 shrink-0 @max-md:w-7">
          <PlaceBadge place={row.disqualified ? null : row.place} unplaced={l.unplaced} />
        </span>
        {prizes ? (
          <span className="w-7 shrink-0">
            <PrizeBadge prize={row.prize} names={l.prizes} compact />
          </span>
        ) : null}
        <span className={WHO}>
          <span className="col-start-1 row-start-1 flex min-w-0 items-center gap-2 @max-md:col-span-2">
            <span
              id={nameId}
              className={cn(
                "min-w-0 truncate text-body",
                row.deleted ? "text-ink-3 italic" : row.disqualified ? "text-ink-3" : "text-ink",
              )}
            >
              {name}
            </span>
            {row.disqualified ? (
              <span className="shrink-0 rounded-full bg-bad-wash px-2 py-0.5 font-mono text-label text-bad uppercase">
                {l.staff.disqualified}
              </span>
            ) : null}
          </span>
          <span className="col-start-1 row-start-2 min-w-0 truncate font-mono text-data text-ink-3">{row.login}</span>
          <span className={NUMBERS}>
            <span className={cn(SOLVED, "font-mono text-data tabular-nums")}>
              <span className="sr-only">{l.columns.solved} </span>
              <span>{row.solved}</span>
            </span>
            <span aria-hidden className="font-mono text-data text-ink-3 @md:hidden">
              ·
            </span>
            <span className={cn(SCORE, "font-mono text-data tabular-nums")}>
              <span className="sr-only">{icpc ? l.columns.penalty : l.columns.points} </span>
              <span>{score ?? "—"}</span>
            </span>
          </span>
        </span>
      </label>
    </li>
  );
});

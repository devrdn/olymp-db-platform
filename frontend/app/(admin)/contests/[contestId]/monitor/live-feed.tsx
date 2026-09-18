"use client";

import { memo, useCallback, useLayoutEffect, useRef, useState } from "react";

import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";
import type { FeedItem } from "@/lib/api/monitor";
import { readableDuration } from "@/lib/format/bytes";
import { DEFAULT_TIME_ZONE, formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import type { FeedState } from "./feed-list";

type MonitorDict = Dictionary["workspace"]["monitor"];

/**
 * The feed's filter, in the groups an organiser thinks in rather than the
 * fifteen kinds the API knows. Selecting none is selecting all.
 */
export const KIND_GROUPS = {
  queries: ["query"],
  answers: ["answer"],
  absences: ["page_left"],
  pastes: ["paste"],
  network: ["ip_changed", "parallel_session"],
  sessions: ["sign_in", "sign_out", "sign_in_failed"],
  tabs: ["tab_created", "tab_renamed", "tab_deleted"],
  clock: ["started", "finished", "disqualified"],
} as const;
type KindGroup = keyof typeof KIND_GROUPS;
const GROUPS = Object.keys(KIND_GROUPS) as KindGroup[];

/**
 * One line's height, in rem. Every line is this tall by construction — two
 * rows of text, clipped rather than wrapped — which is what makes the window
 * below arithmetic, the same trade `result-panel.tsx`'s table makes.
 */
export const FEED_ROW_REM = 3.5;

/** Lines kept rendered past each edge of the view, so a fast scroll never shows a blank band. */
const OVERSCAN = 8;

/** Lines rendered before the box has been measured. */
const UNMEASURED_ROWS = 24;

function rowHeightPx(): number {
  return FEED_ROW_REM * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
}

/**
 * The whole contest's live feed, newest at the bottom (design §6).
 *
 * - **Following.** While the organiser is at the bottom, new lines keep it
 *   there. Scrolling up stops that: the place is kept, new lines are counted,
 *   and a button says how many and jumps back.
 * - **Bounded and windowed.** The list holds at most `FEED_LIMIT` lines
 *   (`feed-list.ts`) and renders only those in view, each at a fixed height,
 *   so a poll that adds five lines lays out five lines — not a thousand.
 *   When lines fall off the top, or older ones arrive above, the offset is
 *   moved by the same number of rows, so what the organiser was reading
 *   stays where it was.
 * - **Older.** Loaded on request, `before=` the oldest line held. Past the
 *   bound the newest end is dropped instead, and the way back to the latest
 *   reads the newest page afresh (`FeedState.detached`).
 */
export function LiveFeed({
  contestId,
  feed,
  kinds,
  onKinds,
  onLoadOlder,
  loadingOlder,
  onToLatest,
  dict,
  locale,
}: {
  contestId: string;
  feed: FeedState;
  kinds: readonly string[];
  onKinds: (kinds: string[]) => void;
  onLoadOlder: () => void;
  loadingOlder: boolean;
  onToLatest: () => void;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor;
  const items = feed.items;
  const total = items.length;

  const scrollRef = useRef<HTMLDivElement>(null);
  const rowHeightRef = useRef(FEED_ROW_REM * 16);
  const [range, setRange] = useState({ start: 0, end: Math.min(total, UNMEASURED_ROWS) });
  // Whether the view is at the newest line, and — once it is not — the
  // newest line it had seen, which is what "N new" counts from.
  const [following, setFollowing] = useState(true);
  const [seen, setSeen] = useState<string | undefined>(undefined);
  const previousItems = useRef(items);

  const measure = useCallback(() => {
    const element = scrollRef.current;
    if (!element) return;
    const rowHeight = rowHeightRef.current;
    const first = Math.max(0, Math.floor(element.scrollTop / rowHeight));
    const fits = Math.ceil(element.clientHeight / rowHeight);
    const start = Math.max(0, first - OVERSCAN);
    const end = Math.min(total, first + fits + OVERSCAN);
    setRange((current) => (current.start === start && current.end === end ? current : { start, end }));
  }, [total]);

  const toBottom = useCallback(() => {
    const element = scrollRef.current;
    if (!element) return;
    element.scrollTop = Math.max(0, total * rowHeightRef.current - element.clientHeight);
  }, [total]);

  const onScroll = () => {
    const element = scrollRef.current;
    if (element) {
      const bottom = total * rowHeightRef.current - element.clientHeight;
      const atBottom = element.scrollTop >= bottom - rowHeightRef.current / 2;
      if (atBottom !== following) {
        setFollowing(atBottom);
        setSeen(atBottom ? undefined : items.at(-1)?.cursor);
      }
    }
    measure();
  };

  // After every change of the list: stay at the bottom while following, and
  // otherwise keep the line being read where it was.
  useLayoutEffect(() => {
    rowHeightRef.current = rowHeightPx();
    const element = scrollRef.current;
    const before = previousItems.current;
    previousItems.current = items;
    if (!element) return;

    if (following) {
      toBottom();
    } else if (before !== items && before.length > 0 && items.length > 0) {
      const rowHeight = rowHeightRef.current;
      const shiftedDown = items.findIndex((item) => item.cursor === before[0].cursor);
      if (shiftedDown > 0) {
        element.scrollTop += shiftedDown * rowHeight;
      } else if (shiftedDown < 0) {
        const droppedAbove = before.findIndex((item) => item.cursor === items[0].cursor);
        if (droppedAbove > 0) element.scrollTop = Math.max(0, element.scrollTop - droppedAbove * rowHeight);
      }
    }
    measure();
    // `following` is read as it stood when the list changed; a change of
    // `following` alone moves nothing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, measure, toBottom]);

  // The box is resized with the window; a taller box needs more lines in it.
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [measure]);

  // What arrived since the organiser scrolled up. A detached list no longer
  // ends at the live tail — its newest end, the line last seen among it, was
  // dropped to make room for older ones — so it counts nothing here; what
  // arrives meanwhile is `feed.missed`, counted as it comes.
  const seenIndex = seen === undefined ? -1 : items.findIndex((item) => item.cursor === seen);
  const unseen =
    feed.detached || following || seen === undefined ? 0 : seenIndex < 0 ? total : total - 1 - seenIndex;

  const jump = () => {
    setFollowing(true);
    setSeen(undefined);
    toBottom();
    measure();
  };

  const selected = new Set(kinds);
  const groupOn = (group: KindGroup) => KIND_GROUPS[group].every((kind) => selected.has(kind));
  const toggle = (group: KindGroup) => {
    const on = GROUPS.filter((g) => (g === group ? !groupOn(g) : groupOn(g)));
    onKinds(on.flatMap((g) => [...KIND_GROUPS[g]]));
  };

  const start = Math.min(range.start, total);
  const end = Math.max(start, Math.min(range.end, total));

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div role="group" aria-label={t.feed.kindsLabel} className="flex flex-wrap gap-1.5">
        <FilterChip pressed={kinds.length === 0} onClick={() => onKinds([])}>
          {t.feed.all}
        </FilterChip>
        {GROUPS.map((group) => (
          <FilterChip key={group} pressed={groupOn(group)} onClick={() => toggle(group)}>
            {t.feed.groups[group]}
          </FilterChip>
        ))}
      </div>

      <div className="flex min-h-(--control-h) items-center">
        {feed.olderAvailable ? (
          <button
            type="button"
            onClick={onLoadOlder}
            disabled={loadingOlder}
            className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
          >
            {loadingOlder ? t.feed.loadingOlder : t.feed.loadOlder}
          </button>
        ) : total > 0 ? (
          <p className="font-mono text-label text-ink-3 uppercase">{t.feed.start}</p>
        ) : null}
      </div>

      {feed.gap > 0 ? (
        <p className="text-small text-warn">{t.feed.gap.replace("{n}", String(feed.gap))}</p>
      ) : null}

      <div className="relative">
        <div
          ref={scrollRef}
          data-testid="feed-scroller"
          onScroll={onScroll}
          className="h-[42rem] overflow-x-hidden overflow-y-auto border-y border-line max-narrow:h-[28rem]"
        >
          {total === 0 ? (
            <p className="p-3 text-body text-ink-2">{t.feed.empty}</p>
          ) : (
            <ol
              aria-label={t.feed.heading}
              className="relative"
              style={{ height: `${total * FEED_ROW_REM}rem` }}
            >
              {items.slice(start, end).map((item, i) => (
                <FeedLine
                  key={item.cursor}
                  item={item}
                  index={start + i}
                  total={total}
                  contestId={contestId}
                  t={t}
                  locale={locale}
                />
              ))}
            </ol>
          )}
        </div>

        {/* Detached, the way back to the latest is always offered: the
            newest lines were dropped, and scrolling down cannot reach them.
            What arrived meanwhile is said beside it, and only that. */}
        {feed.detached ? (
          <button
            type="button"
            onClick={() => {
              onToLatest();
              jump();
            }}
            className={cn(JUMP, "gap-2")}
          >
            {t.feed.toLatest}{" "}
            {feed.missed > 0 ? (
              <span className="font-mono text-label text-accent">
                {t.feed.newItems.replace("{n}", String(feed.missed))}
              </span>
            ) : null}
          </button>
        ) : unseen > 0 ? (
          <button type="button" onClick={jump} className={JUMP}>
            {t.feed.newItems.replace("{n}", String(unseen))}
          </button>
        ) : null}
      </div>
    </div>
  );
}

const JUMP = cn(
  buttonVariants({ variant: "secondary", size: "sm" }),
  "absolute bottom-3 left-1/2 -translate-x-1/2 bg-bg whitespace-nowrap",
);

function FilterChip({
  pressed,
  onClick,
  children,
}: {
  pressed: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        "rounded-full border px-2.5 py-0.5 font-mono text-label uppercase transition-colors duration-(--t-input) ease-standard",
        pressed ? "border-cta bg-cta text-cta-fg" : "border-edge text-ink-2 hover:border-ink-2 hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}

const clockFormatters = new Map<string, Intl.DateTimeFormat>();

/** The time of day to the second: the feed orders things inside one minute. */
function clock(iso: string, locale: string): string {
  let formatter = clockFormatters.get(locale);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
      timeZone: DEFAULT_TIME_ZONE,
    });
    clockFormatters.set(locale, formatter);
  }
  return formatter.format(new Date(iso));
}

const QUIET_KINDS = new Set(["tab_created", "tab_renamed", "tab_deleted"]);

/** The status of a query in the colour of what it means; running is the one live thing. */
const QUERY_TONE: Record<string, string> = {
  running: "text-accent",
  ok: "text-good",
  error: "text-bad",
  rejected: "text-warn",
  timeout: "text-warn",
};

/** One line of the feed, positioned by its index. Memoised on the item. */
const FeedLine = memo(function FeedLine({
  item,
  index,
  total,
  contestId,
  t,
  locale,
}: {
  item: FeedItem;
  index: number;
  total: number;
  contestId: string;
  t: MonitorDict;
  locale: string;
}) {
  const kindLabel = (t.feed.kind as Record<string, string>)[item.kind] ?? item.kind;
  const quiet = QUIET_KINDS.has(item.kind);

  return (
    <li
      aria-setsize={total}
      aria-posinset={index + 1}
      data-quiet={quiet || undefined}
      // Quiet lines are muted by their text colour alone, never by opacity:
      // the muted ink is the quietest the contrast check still passes.
      className="absolute inset-x-0 flex flex-col justify-center gap-0.5 overflow-hidden border-b border-line px-3"
      style={{ top: `${index * FEED_ROW_REM}rem`, height: `${FEED_ROW_REM}rem` }}
    >
      <div className="flex min-w-0 items-baseline gap-2">
        <time
          dateTime={item.at}
          title={formatMoment(item.at, { locale })}
          className="shrink-0 font-mono text-label text-ink-3 tabular-nums"
        >
          {clock(item.at, locale)}
        </time>
        <Link
          href={`/contests/${contestId}/monitor/${item.registrationId}`}
          className={cn("min-w-0 truncate text-small underline-offset-4 hover:underline", quiet ? "text-ink-3" : "text-ink")}
        >
          {item.fullName || item.login}
        </Link>
        <span className="ml-auto shrink-0 font-mono text-label text-ink-3 uppercase">{kindLabel}</span>
      </div>
      <Detail item={item} t={t} quiet={quiet} />
    </li>
  );
});

function fill(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => (name in values ? String(values[name]) : whole));
}

function Detail({ item, t, quiet }: { item: FeedItem; t: MonitorDict; quiet: boolean }) {
  const d = t.feed.describe;
  const line = cn("min-w-0 truncate text-small", quiet ? "text-ink-3" : "text-ink-2");
  const detail = item.detail;

  switch (detail.type) {
    case "query": {
      const first = detail.sql.split("\n").find((part) => part.trim() !== "") ?? detail.sql;
      const status = (t.feed.queryStatus as Record<string, string>)[detail.status] ?? detail.status;
      return (
        <p className={cn(line, "flex gap-2")} title={detail.error ?? detail.sql}>
          <span className={cn("shrink-0 font-mono text-label uppercase", QUERY_TONE[detail.status] ?? "text-ink-3")}>
            {status}
          </span>
          <span className="min-w-0 truncate font-mono text-data text-ink-2">{first}</span>
        </p>
      );
    }
    case "answer":
      return (
        <p className={cn(line, detail.correct ? "text-good" : "text-bad")} title={detail.value}>
          {fill(detail.correct ? d.answerCorrect : d.answerWrong, { n: detail.questionOrd, attempt: detail.attemptNo })}
        </p>
      );
    case "page_left":
      return <p className={line}>{fill(d.pageLeft, { duration: readableDuration(detail.awayMs / 1000) })}</p>;
    case "paste": {
      const target = (d.pasteTarget as Record<string, string>)[detail.target] ?? detail.target;
      return (
        <p className={line} title={detail.text}>
          {fill(d.paste, { chars: detail.chars, target })}
        </p>
      );
    }
    case "ip_changed":
      return <p className={line}>{fill(d.ipChanged, { from: detail.from, to: detail.to })}</p>;
    case "parallel_session":
      return (
        <p className={line} title={detail.userAgent}>
          {fill(d.parallelSession, { ip: detail.otherIp })}
        </p>
      );
    case "audit": {
      const text =
        item.kind === "sign_in"
          ? detail.ip
            ? fill(d.signInFrom, { ip: detail.ip })
            : d.signIn
          : item.kind === "sign_out"
            ? d.signOut
            : item.kind === "sign_in_failed"
              ? d.signInFailed
              : d.disqualified;
      return (
        <p className={line} title={detail.userAgent ?? detail.reason}>
          {text}
        </p>
      );
    }
    case "tab": {
      const text =
        item.kind === "tab_renamed"
          ? fill(d.tabRenamed, { from: detail.from ?? "", to: detail.to ?? "" })
          : fill(item.kind === "tab_deleted" ? d.tabDeleted : d.tabCreated, { title: detail.title ?? "" });
      return <p className={line}>{text}</p>;
    }
    default: {
      const text = item.kind === "started" ? d.started : item.kind === "finished" ? d.finished : d.unknown;
      return <p className={line}>{text}</p>;
    }
  }
}

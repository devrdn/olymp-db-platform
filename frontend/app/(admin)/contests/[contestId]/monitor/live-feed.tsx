"use client";

import { memo, useCallback, useLayoutEffect, useRef, useState } from "react";

import Link from "next/link";

import { QUERY_TONE } from "@/components/product/query-row";
import { buttonVariants } from "@/components/ui/button";
import type { FeedItem } from "@/lib/api/monitor";
import { readableDuration } from "@/lib/format/bytes";
import { formatMoment, formatSeconds } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import type { FeedState } from "./feed-list";

type MonitorDict = Dictionary["workspace"]["monitor"];

/** Filter groups over the API's kinds. Selecting none selects all. */
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

/** Line height in rem. Lines are clipped to two rows, so the virtual window is arithmetic. */
export const FEED_ROW_REM = 3.5;

/** Lines rendered past each edge, so a fast scroll shows no blank band. */
const OVERSCAN = 8;

const UNMEASURED_ROWS = 24;

function rowHeightPx(): number {
  return FEED_ROW_REM * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
}

/**
 * The contest's live feed, newest at the bottom (SPEC.md §5.1). At the bottom it
 * follows new lines; scrolled up it keeps the place and counts new lines behind
 * a jump button. At most `FEED_LIMIT` lines, rendered only in view at a fixed
 * height; when lines are dropped or prepended the offset shifts by the same
 * rows so the reader's place holds. Older lines load `before=` the oldest; past
 * the bound the newest end is dropped (`FeedState.detached`).
 */
export function LiveFeed({
  contestId,
  feed,
  kinds,
  onKinds,
  onLoadOlder,
  loadingOlder,
  onToLatest,
  showParticipant = true,
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
  /** Off on one participant's own page. */
  showParticipant?: boolean;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor;
  const items = feed.items;
  const total = items.length;

  const scrollRef = useRef<HTMLDivElement>(null);
  const rowHeightRef = useRef(FEED_ROW_REM * 16);
  const [range, setRange] = useState({ start: 0, end: Math.min(total, UNMEASURED_ROWS) });
  // At the newest line or not; once not, `seen` is the newest line seen, which
  // "N new" counts from.
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

  // Stay at the bottom while following; otherwise keep the line being read in
  // place.
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
    // A change of `following` alone moves nothing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, measure, toBottom]);

  // A taller box needs more lines.
  useLayoutEffect(() => {
    const element = scrollRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [measure]);

  // New lines since scrolling up. A detached list lost its newest end, so it
  // counts `feed.missed` instead.
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
                  showParticipant={showParticipant}
                  t={t}
                  locale={locale}
                />
              ))}
            </ol>
          )}
        </div>

        {/* Detached, the newest lines are unreachable by scrolling, so the way
           back is always offered. */}
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

/** Time of day to the second, from `lib/format` so the zone rule lives once. */
export function clock(iso: string, locale: string): string {
  return formatSeconds(iso, { locale });
}

const QUIET_KINDS = new Set(["tab_created", "tab_renamed", "tab_deleted"]);

const FeedLine = memo(function FeedLine({
  item,
  index,
  total,
  contestId,
  showParticipant,
  t,
  locale,
}: {
  item: FeedItem;
  index: number;
  total: number;
  contestId: string;
  showParticipant: boolean;
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
      // Muted by text colour, never opacity: the muted ink is the quietest that
      // passes contrast.
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
        {showParticipant ? (
          <Link
            href={`/contests/${contestId}/monitor/${item.registrationId}`}
            className={cn(
              "min-w-0 truncate text-small underline-offset-4 hover:underline",
              quiet ? "text-ink-3" : "text-ink",
            )}
          >
            {item.fullName || item.login}
          </Link>
        ) : null}
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
          {detail.count > 1
            ? fill(d.pasteRepeated, { chars: detail.chars, target, count: detail.count })
            : fill(d.paste, { chars: detail.chars, target })}
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

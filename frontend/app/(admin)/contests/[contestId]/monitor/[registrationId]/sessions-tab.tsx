"use client";

import { memo, useMemo } from "react";

import { buttonVariants } from "@/components/ui/button";
import type { FeedItem, FeedPage } from "@/lib/api/monitor";
import { formatMoment } from "@/lib/format/datetime";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { Problem } from "../monitor-view";
import { useMonitor } from "../use-monitor";

/** The timeline's kinds this tab is made of. */
export const SESSION_KINDS = ["sign_in", "sign_out", "sign_in_failed", "ip_changed", "parallel_session"];

type SessionsDict = Dictionary["workspace"]["monitor"]["participant"]["sessions"];

/** The addresses an item names, in the order it names them. */
function addressesOf(item: FeedItem): string[] {
  const d = item.detail;
  switch (d.type) {
    case "audit":
      return d.ip ? [d.ip] : [];
    case "ip_changed":
      return [d.from, d.to];
    case "parallel_session":
      return [d.otherIp];
    default:
      return [];
  }
}

/**
 * The sign-ins and networks tab (design §6): sign-ins, sign-outs and failed
 * sign-ins with their address and browser, address changes and parallel
 * sessions — the participant's timeline narrowed to those kinds, kept
 * current the way the timeline tab is (`useMonitor`), newest first, with
 * every address seen named once above.
 */
export function SessionsTab({
  contestId,
  registrationId,
  feed: initialFeed,
  dict,
  locale,
}: {
  contestId: string;
  registrationId: string;
  feed: FeedPage;
  dict: Dictionary;
  locale: string;
}) {
  const t = dict.workspace.monitor.participant.sessions;
  const monitor = useMonitor({ contestId, participant: registrationId, feed: initialFeed, kinds: SESSION_KINDS });
  const items = monitor.feed.items;
  const newestFirst = useMemo(() => [...items].reverse(), [items]);
  const addresses = useMemo(() => [...new Set(items.flatMap(addressesOf))], [items]);

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <Problem problem={monitor.problem} t={dict.workspace.monitor} className="empty:-mt-6" />

      <section className="flex min-w-0 flex-col gap-2">
        <h3 className="font-mono text-label text-ink-3 uppercase">{t.addresses}</h3>
        {addresses.length === 0 ? (
          <p className="text-small text-ink-3">{t.noAddresses}</p>
        ) : (
          <ul aria-label={t.addresses} className="flex flex-wrap gap-1.5">
            {addresses.map((address) => (
              <li key={address} className="rounded-full border border-edge px-2 font-mono text-label text-ink-2">
                {address}
              </li>
            ))}
          </ul>
        )}
      </section>

      {items.length === 0 ? (
        <p className="text-body text-ink-2">{t.empty}</p>
      ) : (
        <div className="max-h-[42rem] min-w-0 overflow-auto border-y border-line max-narrow:max-h-[70vh]">
          <table className="w-full min-w-[36rem] border-separate text-left" style={{ borderSpacing: 0 }}>
            <thead>
              <tr>
                {(["when", "event", "address", "browser"] as const).map((column) => (
                  <th
                    key={column}
                    scope="col"
                    className="sticky top-0 border-b border-line-2 bg-bg px-2 py-2 font-mono text-label font-medium text-ink-3 uppercase"
                  >
                    {t.columns[column]}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {newestFirst.map((item) => (
                <SessionRow key={item.cursor} item={item} t={t} locale={locale} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {monitor.feed.olderAvailable ? (
        <button
          type="button"
          onClick={() => void monitor.loadOlder()}
          disabled={monitor.loadingOlder}
          className={cn(buttonVariants({ variant: "quiet", size: "sm" }), "self-start")}
        >
          {monitor.loadingOlder ? dict.workspace.monitor.feed.loadingOlder : dict.workspace.monitor.feed.loadOlder}
        </button>
      ) : null}
    </div>
  );
}

const CELL = "border-b border-line px-2 py-1.5 align-top";

/** One event. Memoised on the item, which a poll that brought nothing new leaves as it was. */
const SessionRow = memo(function SessionRow({ item, t, locale }: { item: FeedItem; t: SessionsDict; locale: string }) {
  const d = item.detail;
  const event = (t.events as Record<string, string>)[item.kind] ?? item.kind;
  const address =
    d.type === "audit" ? (d.ip ?? "") : d.type === "ip_changed" ? `${d.from} → ${d.to}` : d.type === "parallel_session" ? d.otherIp : "";
  const browser = d.type === "audit" ? (d.userAgent ?? "") : d.type === "parallel_session" ? d.userAgent : "";
  const warn = item.kind === "sign_in_failed" || item.kind === "parallel_session" || item.kind === "ip_changed";

  return (
    // Up to a thousand rows (the feed's bound): the browser skips laying out
    // and painting those out of view, as the queries list does.
    <tr className="[contain-intrinsic-size:auto_2.5rem] [content-visibility:auto]">
      <td className={cn(CELL, "font-mono text-label whitespace-nowrap text-ink-2 tabular-nums")}>
        <time dateTime={item.at}>{formatMoment(item.at, { locale })}</time>
      </td>
      <td className={cn(CELL, "text-small whitespace-nowrap", warn ? "text-warn" : "text-ink")}>{event}</td>
      <td className={cn(CELL, "font-mono text-label whitespace-nowrap text-ink-2")}>{address}</td>
      <td className={cn(CELL, "max-w-80 text-small break-words text-ink-3")}>{browser}</td>
    </tr>
  );
});

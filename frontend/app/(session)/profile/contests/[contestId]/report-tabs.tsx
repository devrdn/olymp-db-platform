import Link from "next/link";

import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/** The words this screen is written in. */
export type ReportDict = Dictionary["profile"]["report"];

/** The tabs of a contest report, in the order they stand. */
export const REPORT_TABS = ["summary", "queries", "answers", "notes"] as const;
export type ReportTab = (typeof REPORT_TABS)[number];

/** The tab `?tab=` names; the result when it names none, or nothing this screen has. */
export function tabFromParam(value: string | string[] | undefined): ReportTab {
  const first = Array.isArray(value) ? value[0] : value;
  return (REPORT_TABS as readonly string[]).includes(first ?? "") ? (first as ReportTab) : "summary";
}

/** A tab's address; the result is the screen's own, with no parameter. */
export function tabHref(contestId: string, tab: ReportTab): string {
  const base = `/profile/contests/${contestId}`;
  return tab === "summary" ? base : `${base}?tab=${tab}`;
}

/**
 * The strip of a report's tabs.
 *
 * Links, not buttons: each tab has an address, so the server reads that tab's
 * data and only that tab's before the page arrives, and a student can keep
 * the one they were reading. Four labels do not fit a phone's width, so the
 * strip scrolls sideways inside itself — the page never does, the same fix
 * the monitoring tabs and the play screen's panels have.
 */
export function ReportTabs({
  contestId,
  current,
  t,
}: {
  contestId: string;
  current: ReportTab;
  t: ReportDict;
}) {
  return (
    <nav
      aria-label={t.tabsLabel}
      className="flex min-w-0 gap-6 overflow-x-auto border-b border-line max-narrow:gap-5"
    >
      {REPORT_TABS.map((tab) => (
        <Link
          key={tab}
          href={tabHref(contestId, tab)}
          scroll={false}
          aria-current={tab === current ? "page" : undefined}
          className={cn(
            "-mb-px shrink-0 border-b-2 py-3 text-control whitespace-nowrap transition-colors duration-(--t-input) ease-standard",
            tab === current ? "border-ink text-ink" : "border-transparent text-ink-3 hover:border-line-2 hover:text-ink",
          )}
        >
          {t.tabs[tab]}
        </Link>
      ))}
    </nav>
  );
}

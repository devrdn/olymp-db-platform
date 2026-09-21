import { TabStrip } from "@/components/product/tab-strip";
import type { Dictionary } from "@/lib/i18n/dictionary";

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
 * The strip of a report's tabs: `TabStrip`, which the monitoring page wears
 * too, with this screen's own addresses and words.
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
    <TabStrip
      label={t.tabsLabel}
      tabs={REPORT_TABS.map((tab) => ({ href: tabHref(contestId, tab), label: t.tabs[tab] }))}
      current={tabHref(contestId, current)}
    />
  );
}

import { TabStrip } from "@/components/product/tab-strip";
import type { Dictionary } from "@/lib/i18n/dictionary";

/** This screen's dictionary section. */
export type ReportDict = Dictionary["profile"]["report"];

export const REPORT_TABS = ["summary", "queries", "answers", "notes"] as const;
export type ReportTab = (typeof REPORT_TABS)[number];

/** The tab `?tab=` names, or the summary for none or an unknown one. */
export function tabFromParam(value: string | string[] | undefined): ReportTab {
  const first = Array.isArray(value) ? value[0] : value;
  return (REPORT_TABS as readonly string[]).includes(first ?? "") ? (first as ReportTab) : "summary";
}

/** A tab's address; the summary is the bare page. */
export function tabHref(contestId: string, tab: ReportTab): string {
  const base = `/profile/contests/${contestId}`;
  return tab === "summary" ? base : `${base}?tab=${tab}`;
}

/** `TabStrip` with this screen's addresses and words. */
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

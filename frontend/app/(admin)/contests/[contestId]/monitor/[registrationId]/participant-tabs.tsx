import { TabStrip } from "@/components/product/tab-strip";
import type { Dictionary } from "@/lib/i18n/dictionary";

export const PARTICIPANT_TABS = ["timeline", "queries", "answers", "workspace", "sessions"] as const;
export type ParticipantTab = (typeof PARTICIPANT_TABS)[number];

/** The tab `?tab=` names, or the timeline for none or an unknown one. */
export function tabFromParam(value: string | string[] | undefined): ParticipantTab {
  const first = Array.isArray(value) ? value[0] : value;
  return (PARTICIPANT_TABS as readonly string[]).includes(first ?? "") ? (first as ParticipantTab) : "timeline";
}

/** A tab's shareable address; the timeline is the bare page. */
export function tabHref(contestId: string, registrationId: string, tab: ParticipantTab): string {
  const base = `/contests/${contestId}/monitor/${registrationId}`;
  return tab === "timeline" ? base : `${base}?tab=${tab}`;
}

/** `TabStrip` with this screen's addresses and words. */
export function ParticipantTabs({
  contestId,
  registrationId,
  current,
  dict,
}: {
  contestId: string;
  registrationId: string;
  current: ParticipantTab;
  dict: Dictionary;
}) {
  const t = dict.workspace.monitor.participant;
  return (
    <TabStrip
      label={t.tabsLabel}
      tabs={PARTICIPANT_TABS.map((tab) => ({ href: tabHref(contestId, registrationId, tab), label: t.tabs[tab] }))}
      current={tabHref(contestId, registrationId, current)}
    />
  );
}

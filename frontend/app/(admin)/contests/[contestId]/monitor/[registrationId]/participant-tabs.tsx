import { TabStrip } from "@/components/product/tab-strip";
import type { Dictionary } from "@/lib/i18n/dictionary";

/** The tabs of a participant's page, in the order they stand. */
export const PARTICIPANT_TABS = ["timeline", "queries", "answers", "workspace", "sessions"] as const;
export type ParticipantTab = (typeof PARTICIPANT_TABS)[number];

/** The tab `?tab=` names; the timeline when it names none, or nothing this page has. */
export function tabFromParam(value: string | string[] | undefined): ParticipantTab {
  const first = Array.isArray(value) ? value[0] : value;
  return (PARTICIPANT_TABS as readonly string[]).includes(first ?? "") ? (first as ParticipantTab) : "timeline";
}

/** A tab's address, which an organiser can share; the timeline is the page's own. */
export function tabHref(contestId: string, registrationId: string, tab: ParticipantTab): string {
  const base = `/contests/${contestId}/monitor/${registrationId}`;
  return tab === "timeline" ? base : `${base}?tab=${tab}`;
}

/**
 * The strip of a participant's tabs: `TabStrip`, which a participant's own
 * report wears too, with this screen's own addresses and words.
 */
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

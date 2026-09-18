import Link from "next/link";

import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

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
 * The strip of a participant's tabs: links, so each tab has an address an
 * organiser can share and the server reads that tab's data first. Five
 * labels do not fit a phone's width, so the strip scrolls sideways inside
 * itself — the page never does, the same fix the play screen's panel tabs
 * have.
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
  const names = dict.workspace.monitor.participant.tabs;
  return (
    <nav
      aria-label={dict.workspace.monitor.participant.tabsLabel}
      className="flex min-w-0 gap-6 overflow-x-auto border-b border-line max-narrow:gap-5"
    >
      {PARTICIPANT_TABS.map((tab) => (
        <Link
          key={tab}
          href={tabHref(contestId, registrationId, tab)}
          scroll={false}
          aria-current={tab === current ? "page" : undefined}
          className={cn(
            "-mb-px shrink-0 border-b-2 py-3 text-control whitespace-nowrap transition-colors duration-(--t-input) ease-standard",
            tab === current ? "border-ink text-ink" : "border-transparent text-ink-3 hover:border-line-2 hover:text-ink",
          )}
        >
          {names[tab]}
        </Link>
      ))}
    </nav>
  );
}

import { notFound } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import {
  answersSchema,
  feedSchema,
  MAX_FEED_PAGE,
  participantSchema,
  queriesSchema,
  rosterSchema,
  timelinePath,
  workspaceSchema,
} from "@/lib/api/monitor";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContestResource } from "../../contest";
import { AnswersTab } from "./answers-tab";
import { ParticipantHeader } from "./participant-header";
import { ParticipantTabs, tabFromParam, type ParticipantTab } from "./participant-tabs";
import { QueriesTab } from "./queries-tab";
import { SESSION_KINDS, SessionsTab } from "./sessions-tab";
import { TimelineTab } from "./timeline-tab";
import { WorkspaceTab } from "./workspace-tab";

/**
 * Everything one participant did (design §6, second item): a heading, and
 * five tabs — the timeline, the SQL queries, the answers with the queries
 * that led to them, the workspace with its history, and sign-ins and
 * networks.
 *
 * The tab is in the address (`?tab=`), so a link to it can be shared, and
 * the server reads that tab's data — and only that tab's — before the page
 * arrives. Each view is audited by the API; nothing here needs to.
 *
 * Every read goes through `loadContestResource`, which answers a missing
 * permission as a 404 page. The API answers a registration of another
 * contest exactly as one that does not exist — 404, with a code of its own
 * (`monitor_participant_not_found`) — so any 404 here is the not-found page
 * too, not an error to retry.
 */
export default async function ParticipantPage(
  props: PageProps<"/contests/[contestId]/monitor/[registrationId]">,
) {
  const [{ contestId, registrationId }, search, locale, dict] = await Promise.all([
    props.params,
    props.searchParams,
    activeLocale(),
    activeDictionary(),
  ]);
  if (!isId(registrationId)) notFound();
  const tab = tabFromParam(search.tab);
  const one = `/monitor/participants/${registrationId}`;

  const [participant, roster, panel] = await Promise.all([
    read(contestId, one, (payload) => participantSchema.parse(payload)),
    read(contestId, "/monitor/participants", (payload) => rosterSchema.parse(payload)),
    loadTab(tab, contestId, registrationId),
  ]);
  const flags = roster.rows.find((row) => row.registrationId === registrationId)?.flags ?? null;
  const names = dict.workspace.monitor.participant.tabs;

  return (
    <div className="@container flex min-w-0 flex-col gap-8">
      <ParticipantHeader contestId={contestId} participant={participant} flags={flags} dict={dict} locale={locale} />
      <div className="flex min-w-0 flex-col gap-6">
        <ParticipantTabs contestId={contestId} registrationId={registrationId} current={tab} dict={dict} />
        <section aria-label={names[tab]} className="min-w-0">
          {panel.tab === "timeline" ? (
            <TimelineTab
              key={registrationId}
              contestId={contestId}
              registrationId={registrationId}
              feed={panel.data}
              dict={dict}
              locale={locale}
            />
          ) : panel.tab === "queries" ? (
            <QueriesTab
              key={registrationId}
              contestId={contestId}
              registrationId={registrationId}
              initial={panel.data}
              dict={dict}
              locale={locale}
            />
          ) : panel.tab === "answers" ? (
            <AnswersTab answers={panel.data} dict={dict} locale={locale} />
          ) : panel.tab === "workspace" ? (
            <WorkspaceTab
              key={registrationId}
              contestId={contestId}
              registrationId={registrationId}
              workspace={panel.data}
              dict={dict}
              locale={locale}
            />
          ) : (
            <SessionsTab
              key={registrationId}
              contestId={contestId}
              registrationId={registrationId}
              feed={panel.data}
              dict={dict}
              locale={locale}
            />
          )}
        </section>
      </div>
    </div>
  );
}

/** One monitoring read, with every 404 answered as the not-found page. */
async function read<T>(contestId: string, path: string, parse: (payload: unknown) => T): Promise<T> {
  let value: T | null;
  try {
    value = await loadContestResource(contestId, path, parse);
  } catch (error: unknown) {
    if (error instanceof ApiError && error.status === 404) notFound();
    throw error;
  }
  if (value === null) notFound();
  return value;
}

/** A timeline read's path under the contest, with the feed's own parameters. */
function timeline(contestId: string, registrationId: string, kinds: string[] = []): string {
  return timelinePath(contestId, registrationId, { kinds, limit: MAX_FEED_PAGE }).slice(`/contests/${contestId}`.length);
}

/** What the tab in the address shows first, read on the server. */
async function loadTab(tab: ParticipantTab, contestId: string, registrationId: string) {
  const one = `/monitor/participants/${registrationId}`;
  switch (tab) {
    case "queries":
      return { tab, data: await read(contestId, `${one}/queries`, (p) => queriesSchema.parse(p)) } as const;
    case "answers":
      return { tab, data: await read(contestId, `${one}/answers`, (p) => answersSchema.parse(p)) } as const;
    case "workspace":
      return { tab, data: await read(contestId, `${one}/workspace`, (p) => workspaceSchema.parse(p)) } as const;
    case "sessions":
      return {
        tab,
        data: await read(contestId, timeline(contestId, registrationId, SESSION_KINDS), (p) => feedSchema.parse(p)),
      } as const;
    default:
      return {
        tab: "timeline",
        data: await read(contestId, timeline(contestId, registrationId), (p) => feedSchema.parse(p)),
      } as const;
  }
}

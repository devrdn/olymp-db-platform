import { managerListSchema, participantListSchema } from "@/lib/api/people";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { ManagerPanel, ParticipantPanel } from "./people-panels";

/**
 * Staff and participants on one screen. Each list has its own permission, so a
 * forbidden one is shown as absent rather than failing the screen.
 */
export default async function PeoplePage(props: PageProps<"/contests/[contestId]/people">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  const [contest, managers, participants] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/managers", (payload) =>
      managerListSchema.parse(payload),
    ).catch(() => null),
    loadContestResource(contestId, "/participants", (payload) =>
      participantListSchema.parse(payload),
    ).catch(() => null),
  ]);

  const t = dict.workspace.people;

  return (
    <div className="flex flex-col gap-12">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>

      {managers ? (
        <ManagerPanel contestId={contest.id} managers={managers.items} dict={dict} />
      ) : null}

      {participants ? (
        <ParticipantPanel
          contestId={contest.id}
          participants={participants.items}
          total={participants.total}
          locale={locale}
          scoring={contest.scoring}
          dict={dict}
        />
      ) : null}

      {!managers && !participants ? (
        <p className="max-w-body text-body text-ink-3">{t.noAccess}</p>
      ) : null}
    </div>
  );
}

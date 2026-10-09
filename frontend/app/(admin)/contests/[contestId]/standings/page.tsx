import { staffStandingsSchema } from "@/lib/api/leaderboard";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { StaffStandingsView } from "./staff-standings";

/**
 * The staff leaderboard, at /standings because /contests/{id}/leaderboard is
 * the public page and two route groups may not share an address.
 */
export default async function StandingsPage(props: PageProps<"/contests/[contestId]/standings">) {
  const [{ contestId }, locale, dict] = await Promise.all([props.params, activeLocale(), activeDictionary()]);

  const [contest, standings] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/leaderboard/live", (payload) => staffStandingsSchema.parse(payload)),
  ]);
  const t = dict.leaderboard;

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{dict.workspace.tabs.leaderboard}</h2>
        <p className="max-w-body text-body text-ink-2">{t.staff.lede}</p>
      </div>
      {standings ? (
        <StaffStandingsView
          contestId={contest.id}
          status={contest.status}
          standings={standings}
          dict={dict}
          locale={locale}
        />
      ) : null}
    </div>
  );
}

import { hasEnded } from "@/lib/api/contests";
import { gameInstancesSchema, gameSchema } from "@/lib/api/game";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { GameDatabases } from "./game-databases";

/**
 * The contest's databases: the spare pool and each participant's copy, read
 * while the contest runs.
 *
 * `/game` is read only to tell the two reasons for having none apart: `absent`
 * (not built yet) is ordinary, while a 404 means the installation has no game
 * cluster, which gets its own sentence.
 */
export default async function DatabasesPage(props: PageProps<"/contests/[contestId]/databases">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  const [contest, game, databases] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/game", (payload) => gameSchema.parse(payload), {
      absent: ["not_found"],
    }),
    // Best-effort, so a briefly unreachable cluster does not take the page
    // down, but `null` is distinct from an empty list: "no databases yet"
    // would be a false statement in the middle of an incident.
    loadContestResource(contestId, "/game/instances", (payload) => gameInstancesSchema.parse(payload), {
      bestEffort: true,
    }),
  ]);

  const t = dict.workspace.databases;

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>

      {game === null ? (
        <p className="max-w-body text-body text-ink-2">{dict.workspace.game.unavailable}</p>
      ) : databases === null ? (
        <p role="alert" className="max-w-body text-body text-bad">
          {t.unreachable}
        </p>
      ) : (
        <GameDatabases
          contestId={contest.id}
          databases={databases}
          ended={hasEnded(contest.status)}
          locale={locale}
          dict={dict}
        />
      )}
    </div>
  );
}

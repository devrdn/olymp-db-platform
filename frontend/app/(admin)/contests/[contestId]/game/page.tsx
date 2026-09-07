import { gameInstancesSchema, gameSchema, gameScriptSchema } from "@/lib/api/game";
import { contentEditable } from "@/lib/api/contests";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { GameDatabases } from "./game-databases";
import { GameEditor } from "./game-editor";

/**
 * The contest's game database.
 *
 * The status and the script are two reads because they are two different
 * things to ask for: the status is polled while a build runs, and the script
 * is up to half a mebibyte that only matters when somebody opens this page to
 * edit it.
 *
 * A contest with no game at all is the ordinary state of a draft, not a wrong
 * address — the API answers `absent` rather than 404 for exactly that reason,
 * so there is one shape here instead of two.
 *
 * The databases that already exist are a third read, below the editor. The
 * editor is about the game everybody gets; the list is about the copies
 * individual people already have, which is what an organiser comes here for
 * once the contest is running rather than while it is being written.
 */
export default async function GamePage(props: PageProps<"/contests/[contestId]/game">) {
  const [{ contestId }, locale, dict] = await Promise.all([
    props.params,
    activeLocale(),
    activeDictionary(),
  ]);

  const [contest, game, script, databases] = await Promise.all([
    loadContest(contestId),
    // `notFoundIsEmpty` because a 404 here is not a wrong address: the game
    // endpoints are mounted only where a game cluster is configured, so an
    // installation without one answers 404 for every contest. Left to become
    // a not-found page, that told an organiser their link was wrong when the
    // truth is that this deployment has nowhere to build a game.
    loadContestResource(contestId, "/game", (payload) => gameSchema.parse(payload), {
      notFoundIsEmpty: true,
    }),
    loadContestResource(contestId, "/game/script", (payload) => gameScriptSchema.parse(payload), {
      notFoundIsEmpty: true,
    }),
    // Caught rather than thrown, the same way the people screen treats each of
    // its two lists: this list reaches the game cluster for its sizes, and the
    // editor above is what an author came for while a contest is being
    // written. One read being unavailable must not take the other away.
    loadContestResource(contestId, "/game/instances", (payload) =>
      gameInstancesSchema.parse(payload),
    ).catch(() => null),
  ]);

  const t = dict.workspace.game;

  return (
    <div className="flex flex-col gap-12">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>

      {game === null ? (
        <p className="max-w-body text-body text-ink-2">{t.unavailable}</p>
      ) : (
        <GameEditor
          contestId={contestId}
          initial={game}
          initialScript={script?.script ?? ""}
          editable={contentEditable(contest.status)}
          dict={dict}
        />
      )}

      {game !== null && databases !== null ? (
        <GameDatabases contestId={contestId} databases={databases} locale={locale} dict={dict} />
      ) : null}
    </div>
  );
}

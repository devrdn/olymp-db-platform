import { gameSchema, gameScriptSchema } from "@/lib/api/game";
import { contentEditable } from "@/lib/api/contests";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { GameEditor } from "./game-editor";

/**
 * The SQL an olympiad's game is built from.
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
 * The databases this game has produced — the spare pool, and each
 * participant's own copy — used to be a third read below the editor. They
 * moved to their own `databases` section: writing this script is authoring,
 * done while the contest is still being put together, and the databases are
 * what running it produces, read by an organiser once the contest is live.
 * Splitting the two sections is what let each keep to its own job instead of
 * one page doing both.
 */
export default async function GamePage(props: PageProps<"/contests/[contestId]/game">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, game, script] = await Promise.all([
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
    </div>
  );
}

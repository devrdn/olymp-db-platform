import { gameSchema, gameScriptSchema } from "@/lib/api/game";
import { contentEditable } from "@/lib/api/contests";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { GameEditor } from "./game-editor";

/** What the API answers for a contest whose game has never been written. */
const ABSENT = gameSchema.parse({ status: "absent" });

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
 */
export default async function GamePage(props: PageProps<"/contests/[contestId]/game">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, game, script] = await Promise.all([
    loadContest(contestId),
    loadContestResource(contestId, "/game", (payload) => gameSchema.parse(payload)),
    loadContestResource(contestId, "/game/script", (payload) => gameScriptSchema.parse(payload)),
  ]);

  const t = dict.workspace.game;

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">{t.lede}</p>
      </div>

      {/* `loadContestResource` is typed to allow null because it can be told
          to read a 404 as "nothing written yet". It is not told that here, and
          it does not need to be: a contest with no game answers 200 with the
          status `absent`. The fallbacks are what that answer would have said,
          so an impossible null renders the same screen rather than a crash. */}
      <GameEditor
        contestId={contestId}
        initial={game ?? ABSENT}
        initialScript={script?.script ?? ""}
        editable={contentEditable(contest.status)}
        dict={dict}
      />
    </div>
  );
}

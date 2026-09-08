import { gameInstancesSchema, gameSchema } from "@/lib/api/game";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { GameDatabases } from "./game-databases";

/**
 * The databases one contest owns: the spare pool, and the copy each
 * participant works in.
 *
 * Split from `game` (§ its own doc comment) because writing the SQL a game is
 * built from is authoring, and this — who holds which running copy, and
 * dropping one — is what happens afterwards, while the contest is live. An
 * organiser reaches for this screen once things are running, not while the
 * game is still being written, and the workspace's navigation now says so by
 * putting them in different groups.
 *
 * `/game` is read here too, though nothing of its answer is shown: its only
 * job is telling apart the two reasons this contest could have no databases.
 * `absent` (no script ever saved) is the ordinary state of a game not yet
 * built, and `GameDatabases` already says that in its own words for an empty
 * list. A 404 here is different — the whole installation has no game cluster
 * configured, which `/game` and `/game/instances` are both gated on together
 * (they are mounted as one unit, or not at all) — so nothing at either
 * address exists to look at, and that gets a sentence of its own rather than
 * an empty table pretending a game could still be built.
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
      notFoundIsEmpty: true,
    }),
    // Caught rather than thrown: a game cluster that is configured but
    // unreachable for a moment must not take the whole page down with it.
    // What it must not do either is pass for an answer. This is the one
    // screen an organiser opens while something is going wrong in the middle
    // of an olympiad, and "this contest has no databases yet" — the sentence
    // it used to fall back to — states as a fact about every contest exactly
    // the thing nobody could find out. So the failure keeps its own shape
    // (null, distinct from an empty list) and is written to the server log,
    // which is otherwise the only place it existed at all.
    loadContestResource(contestId, "/game/instances", (payload) =>
      gameInstancesSchema.parse(payload),
    ).catch((error: unknown) => {
      console.error("reading the game databases of contest %s failed", contestId, error);
      return null;
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
        <GameDatabases contestId={contest.id} databases={databases} locale={locale} dict={dict} />
      )}
    </div>
  );
}

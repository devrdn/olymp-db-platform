import {
  definitionSchema,
  gameSchema,
  gameScriptSchema,
  tableDataSchema,
  tableRowWindowSchema,
  uploadSchema,
  type TableData,
} from "@/lib/api/game";
import { contentEditable } from "@/lib/api/contests";
import { serverRequest } from "@/lib/api/server";
import { activeDictionary } from "@/lib/i18n/server";

import { loadContest, loadContestResource } from "../contest";
import { GameBuild } from "./game-build";
import { GameBuilder } from "./game-builder";
import { GameEditor } from "./game-editor";
import { GameUpload } from "./game-upload";

/**
 * Whether a table already holds data, before the builder's own definition
 * editor renders — the fact `GameBuilder`'s own doc explains a save that
 * changes a locked table's name, columns or primary key is refused for
 * (`ErrDefinitionTableLocked`, server-side). This screen still disables the
 * edit client-side rather than only relying on that refusal — a request
 * that comes back "no" is a worse experience than never offering the edit —
 * which is what this count is read for.
 *
 * A best-effort read, not `loadContestResource`: this is a page load reading
 * up to `MaxDefinitionTables` (fifty) small, indexed windows in parallel to
 * explain a lock *before* anyone clicks anything, not a resource this page
 * depends on to render at all — the same reasoning `initialUpload`'s own
 * `.catch(() => null)` gives below for a read that must not turn a minor
 * hiccup into a 404 for the whole page. `max_rows=1` is every byte this call
 * needs: only whether the table is empty, never its contents.
 */
async function tableRowCount(contestId: string, table: string): Promise<number> {
  try {
    const payload = await serverRequest(
      `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/window?max_rows=1`,
    );
    return tableRowWindowSchema.parse(payload).totalRows;
  } catch {
    return 0;
  }
}

/**
 * A table's own chunked CSV upload a reloaded page finds still receiving,
 * or null — `initialUpload` below, mirrored for one table's own file
 * instead of a whole dump (`GET .../tables/{table}/data/current`,
 * `game_handler.go`'s own route added for exactly this: before it existed,
 * an unfinished table upload became invisible after a reload, and a chunk
 * refused as out of order had no server count left to resynchronise
 * against — `game-builder-table.tsx`'s own doc used to name the gap
 * directly).
 *
 * Best-effort, the identical reasoning `initialUpload`'s own `.catch(() =>
 * null)` gives: a page load must not fail over this.
 */
async function tableCurrentUpload(contestId: string, table: string): Promise<TableData | null> {
  try {
    const payload = await serverRequest(
      `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/current`,
    );
    return tableDataSchema.parse(payload);
  } catch {
    return null;
  }
}

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

  // A separate, best-effort read rather than a third branch of the
  // `Promise.all` above: `GET .../uploads/current` answers `game_uploads_
  // disabled` (404) on an installation with no upload volume configured,
  // which is not a reason to fail this whole page — `game.uploadLimits.
  // enabled` already says the same thing in a shape `GameUpload` renders
  // instead of crashing on. Skipped entirely once the game itself could not
  // be read, the same reason `databases/page.tsx` skips its own instances
  // read: there is nothing for either half of the screen to show.
  const initialUpload =
    game === null
      ? null
      : await loadContestResource(contestId, "/game/uploads/current", (payload) =>
          uploadSchema.parse(payload),
        ).catch(() => null);

  // The third way to build this contest's game: `/game/definition` always
  // answers 200 (an empty definition for a contest with no game, or one
  // built the other two ways — `definition`'s own doc on the API side), the
  // same "one shape either way" convention `game` and `script` already
  // follow above. `notFoundIsEmpty` only guards the one case where this
  // whole handler is unmounted (no game cluster configured), which `game
  // === null` already answered for.
  const definition =
    game === null
      ? null
      : await loadContestResource(contestId, "/game/definition", (payload) =>
          definitionSchema.parse(payload),
        ).catch(() => null);

  // See tableRowCount's own doc. Skipped entirely when the table-data volume
  // is not configured (there is nothing to be empty of) or the definition
  // has no tables yet (nothing to check) — the same "read only what the
  // screen needs" reasoning `initialUpload` above already follows.
  //
  // Read alongside tableCurrentUpload, table by table, rather than as a
  // second fan-out over the same list: both are the same "one table's own
  // best-effort read" this page already makes once per table.
  //
  // The number of round trips is unchanged by this — two per table either
  // way, up to a hundred at the definition's own fifty-table ceiling. What
  // changes is when they happen: pairing them means a table's two reads
  // overlap and the page waits for the slowest table rather than for one
  // whole fan-out and then another. Cutting the count itself would take an
  // endpoint that answers for every table at once, which the API does not
  // offer.
  const tableRowCounts: Record<string, number> = {};
  const tableCurrentUploads: Record<string, TableData | null> = {};
  if (definition && definition.builderLimits.enabled && definition.tables.length > 0) {
    const perTable = await Promise.all(
      definition.tables.map(async (table) => {
        const [rowCount, currentUpload] = await Promise.all([
          tableRowCount(contestId, table.name),
          tableCurrentUpload(contestId, table.name),
        ]);
        return { name: table.name, rowCount, currentUpload };
      }),
    );
    for (const { name, rowCount, currentUpload } of perTable) {
      tableRowCounts[name] = rowCount;
      tableCurrentUploads[name] = currentUpload;
    }
  }

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
        <>
          <GameEditor
            contestId={contestId}
            initial={game}
            initialScript={script?.script ?? ""}
            editable={contentEditable(contest.status)}
            dict={dict}
          />
          <GameUpload
            contestId={contestId}
            game={game}
            initialUpload={initialUpload}
            editable={contentEditable(contest.status)}
            dict={dict}
          />
          {definition ? (
            <GameBuilder
              contestId={contestId}
              definition={definition}
              rowCounts={tableRowCounts}
              currentTableUploads={tableCurrentUploads}
              editable={contentEditable(contest.status)}
              dict={dict}
            />
          ) : null}
          <GameBuild
            contestId={contestId}
            game={game}
            editable={contentEditable(contest.status)}
            dict={dict}
          />
        </>
      )}
    </div>
  );
}

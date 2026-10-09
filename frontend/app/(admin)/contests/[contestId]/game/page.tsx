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
 * Whether a table holds data, so the builder can disable locked edits before
 * the server would refuse them. Best-effort, not `loadContestResource`: up to
 * fifty small reads must not 404 the page. `max_rows=1` is all it needs.
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

/** A table's upload still receiving, for resuming after a reload. Best-effort. */
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
 * The game's SQL. Status and script are separate reads: the status is polled
 * during a build, the script (up to 512 KiB) matters only when editing. A draft
 * without a game is answered as `absent`, not 404.
 */
export default async function GamePage(props: PageProps<"/contests/[contestId]/game">) {
  const [{ contestId }, dict] = await Promise.all([props.params, activeDictionary()]);

  const [contest, game, script] = await Promise.all([
    loadContest(contestId),
    // A 404 means this installation has no game cluster, not a wrong address.
    loadContestResource(contestId, "/game", (payload) => gameSchema.parse(payload), {
      notFoundIsEmpty: true,
    }),
    loadContestResource(contestId, "/game/script", (payload) => gameScriptSchema.parse(payload), {
      notFoundIsEmpty: true,
    }),
  ]);

  // Best-effort: the endpoint answers 404 without an upload volume, and
  // `game.uploadLimits.enabled` already says so. Skipped when the game could
  // not be read.
  const initialUpload =
    game === null
      ? null
      : await loadContestResource(contestId, "/game/uploads/current", (payload) =>
          uploadSchema.parse(payload),
        ).catch(() => null);

  // `/game/definition` always answers 200; `notFoundIsEmpty` only covers a
  // missing game cluster, already handled by `game === null`.
  const definition =
    game === null
      ? null
      : await loadContestResource(contestId, "/game/definition", (payload) =>
          definitionSchema.parse(payload),
        ).catch(() => null);

  // Skipped without a table-data volume or tables. Each table's two reads are
  // paired so the page waits for the slowest table, not two sequential
  // fan-outs; the API has no all-tables endpoint.
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

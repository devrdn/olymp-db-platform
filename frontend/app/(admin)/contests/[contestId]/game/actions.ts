"use server";

import {
  gameSchema,
  tableDataSchema,
  tableRowWindowSchema,
  uploadSchema,
  uploadWindowSchema,
  type Game,
  type TableData,
  type TableRowWindow,
  type Upload,
  type UploadWindow,
} from "@/lib/api/game";
import { isId, pathSegment } from "@/lib/api/ids";
import type { Failure } from "@/lib/api/outcome";
import { serverAction, serverOutcome, serverRequest, type ActionState, type Revalidation } from "@/lib/api/server";

/**
 * `detail` is the API's message, shown beside (never instead of) the
 * dictionary sentence for refusals whose sentence is generic because the table,
 * column or row at fault is specific to this attempt.
 */
export type GameState = ActionState & { detail?: string };

/** A refusal code, or what the write produced (an upload id, a game's status). */
export type UploadActionResult<T> = ActionState & { value?: T; detail?: string };

/** The publish gate and this section both depend on the game. */
function changed(contestId: string): Revalidation[] {
  return [{ layout: `/contests/${contestId}` }];
}

/** A refusal that keeps the API's message as `detail`. */
function withDetail(failure: Failure): { code: string; detail?: string } {
  return failure.detail === undefined ? { code: failure.code } : { code: failure.code, detail: failure.detail };
}

/** One call for the upload and builder controls: what the write produced, or the refusal's code. */
function control<T>(call: () => Promise<T>, revalidate: readonly Revalidation[]): Promise<UploadActionResult<T>> {
  return serverAction(call, { revalidate, success: (value) => ({ ok: true, value }) });
}

/**
 * `control` for the refusals that name the table, column or row at fault, so
 * `detail` crosses too: read from the outcome itself, since an action's own
 * `failure` is never given the API's message.
 */
async function controlNamingFault<T>(
  call: () => Promise<T>,
  revalidate: readonly Revalidation[],
): Promise<UploadActionResult<T>> {
  const result = await serverOutcome(call, { revalidate });
  return result.ok ? { ok: true, value: result.value } : withDetail(result);
}

/** A table's own path, or null for a name that is no path segment. */
function tablePath(contestId: string, table: string): string | null {
  const segment = pathSegment(table);
  return segment === null ? null : `/contests/${contestId}/game/tables/${segment}`;
}

/** What a read the page already has an answer for gives on any failure: nothing. */
async function orNull<T>(call: () => Promise<T>): Promise<T | null> {
  const result = await serverOutcome(call);
  return result.ok ? result.value : null;
}

/**
 * Stores the game's SQL. The API answers 202 and a background worker builds,
 * which takes seconds; the screen watches the status afterwards.
 */
export async function saveGameScriptAction(_previous: GameState, form: FormData): Promise<GameState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  // Not trimmed: the SQL is the author's, and the server refuses a
  // whitespace-only script with a known code.
  const script = String(form.get("script") ?? "");

  return serverAction(
    () => serverRequest(`/contests/${contestId}/game/script`, { method: "PUT", body: { script } }),
    { revalidate: changed(contestId) },
  );
}

/**
 * The build's current state, through a Server Action so the session cookie and
 * the API address stay server-side.
 */
export async function gameStatusAction(contestId: string): Promise<Game | null> {
  if (!isId(contestId)) return null;
  return orNull(async () => gameSchema.parse(await serverRequest(`/contests/${contestId}/game`)));
}

/**
 * Requests a rebuild (`POST .../game/build`), the only way rows typed in the
 * table builder reach a database. Answers 202 with the game.
 */
export async function requestGameBuildAction(contestId: string): Promise<UploadActionResult<Game>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  return controlNamingFault(
    async () => gameSchema.parse(await serverRequest(`/contests/${contestId}/game/build`, { method: "POST" })),
    changed(contestId),
  );
}

// Building from an uploaded dump. Only the control calls live here; chunks are
// `PUT` straight from the browser (`game-upload.tsx`), because a Server
// Action's body limit is far below the configured chunk size.

/**
 * Begins an upload (`POST .../uploads`). `filename` and `declaredBytes` are the
 * browser's claims; the server checks `declaredBytes` against what arrives.
 */
export async function beginGameUploadAction(
  contestId: string,
  filename: string,
  declaredBytes: number,
): Promise<UploadActionResult<Upload>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  // Another tab or a racing reload must see the "receiving" row.
  return control(
    async () =>
      uploadSchema.parse(
        await serverRequest(`/contests/${contestId}/game/uploads`, {
          method: "POST",
          body: { filename, declared_bytes: declaredBytes },
        }),
      ),
    changed(contestId),
  );
}

/**
 * The upload still receiving, if any, so a reloaded page resumes from the
 * server's state rather than the browser's. `null` on any failure: the page
 * already has its initial answer.
 */
export async function currentGameUploadAction(contestId: string): Promise<Upload | null> {
  if (!isId(contestId)) return null;
  return orNull(async () => uploadSchema.parse(await serverRequest(`/contests/${contestId}/game/uploads/current`)));
}

/** A slice of a completed upload's lines (`GET .../uploads/{id}/window`) for the viewer. */
export async function gameUploadWindowAction(
  contestId: string,
  uploadId: string,
  fromLine: number,
): Promise<UploadActionResult<UploadWindow>> {
  if (!isId(contestId) || !isId(uploadId)) return { code: "game_upload_not_found" };

  return control(
    async () =>
      uploadWindowSchema.parse(
        await serverRequest(`/contests/${contestId}/game/uploads/${uploadId}/window?from=${fromLine}`),
      ),
    [],
  );
}

/**
 * Completes an upload (`POST .../complete`), replacing the game like
 * `saveGameScriptAction`, so it revalidates the same path.
 */
export async function completeGameUploadAction(
  contestId: string,
  uploadId: string,
): Promise<UploadActionResult<Game>> {
  if (!isId(contestId) || !isId(uploadId)) return { code: "game_upload_not_found" };

  return control(
    async () =>
      gameSchema.parse(
        await serverRequest(`/contests/${contestId}/game/uploads/${uploadId}/complete`, { method: "POST" }),
      ),
    changed(contestId),
  );
}

/**
 * Cancels an upload still receiving (`POST .../abort`); the server refuses one
 * already complete.
 */
export async function abortGameUploadAction(
  contestId: string,
  uploadId: string,
): Promise<UploadActionResult<never>> {
  if (!isId(contestId) || !isId(uploadId)) return { code: "game_upload_not_found" };

  // Frees the "one receiving upload" slot for a reload's read.
  return serverAction(
    () => serverRequest(`/contests/${contestId}/game/uploads/${uploadId}/abort`, { method: "POST" }),
    { revalidate: changed(contestId) },
  );
}

// Building from a structural description of tables and columns
// (`game-builder.tsx`). A table's CSV chunks are `PUT` from the browser for the
// same body-limit reason as dumps.

/**
 * Stores the definition (`PUT .../game/definition`), replacing the game like
 * `saveGameScriptAction`. The draft is a tree edited in memory, so it travels
 * as one JSON string in a hidden field.
 */
export async function saveGameDefinitionAction(_previous: GameState, form: FormData): Promise<GameState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  let definition: unknown;
  try {
    definition = JSON.parse(String(form.get("definition") ?? "{}"));
  } catch {
    return { code: "invalid_request" };
  }

  // Several validation refusals name the table or column at fault.
  const saved = await serverOutcome(
    () => serverRequest(`/contests/${contestId}/game/definition`, { method: "PUT", body: definition }),
    { revalidate: changed(contestId) },
  );
  return saved.ok ? { ok: true } : withDetail(saved);
}

/**
 * A table's CSV upload still receiving, if any, so a reload resumes from the
 * server's state. `null` on any failure.
 */
export async function currentTableUploadAction(contestId: string, table: string): Promise<TableData | null> {
  if (!isId(contestId)) return null;
  const at = tablePath(contestId, table);
  if (!at) return null;
  return orNull(async () =>
    tableDataSchema.parse(await serverRequest(`${at}/data/current`)),
  );
}

/**
 * Begins a table's CSV upload (`POST .../game/tables/{table}/data`). Shares the
 * server's begin budget (`allowUploadBegin`) with dump uploads.
 */
export async function beginTableUploadAction(
  contestId: string,
  table: string,
  declaredBytes: number,
): Promise<UploadActionResult<TableData>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };
  const at = tablePath(contestId, table);
  if (!at) return { code: "invalid_request" };

  return control(
    async () =>
      tableDataSchema.parse(
        await serverRequest(`${at}/data`, {
          method: "POST",
          body: { declared_bytes: declaredBytes },
        }),
      ),
    [],
  );
}

/**
 * Completes a table's CSV upload (`POST .../data/{id}/complete`). 200, not 202:
 * table data is loaded at the next whole-game build, so there is nothing to
 * watch.
 */
export async function completeTableUploadAction(
  contestId: string,
  table: string,
  id: string,
): Promise<UploadActionResult<TableData>> {
  if (!isId(contestId) || !isId(id)) return { code: "game_table_data_not_found" };
  const at = tablePath(contestId, table);
  if (!at) return { code: "invalid_request" };

  // The file marks the built game out of date on the server, and `page.tsx`
  // renders that notice, so its cached output must be refreshed. The server's
  // full validation names the row and column at fault.
  return controlNamingFault(
    async () =>
      tableDataSchema.parse(
        await serverRequest(`${at}/data/${id}/complete`, {
          method: "POST",
        }),
      ),
    changed(contestId),
  );
}

/** Cancels a table's CSV upload before it finished (`POST .../data/{id}/abort`). */
export async function abortTableUploadAction(
  contestId: string,
  table: string,
  id: string,
): Promise<UploadActionResult<TableData>> {
  if (!isId(contestId) || !isId(id)) return { code: "game_table_data_not_found" };
  const at = tablePath(contestId, table);
  if (!at) return { code: "invalid_request" };

  return control(
    async () =>
      tableDataSchema.parse(
        await serverRequest(`${at}/data/${id}/abort`, {
          method: "POST",
        }),
      ),
    [],
  );
}

/** A page of a table's current rows (`GET .../data/window`), previewed before any build. */
export async function gameTableDataWindowAction(
  contestId: string,
  table: string,
  fromRow: number,
): Promise<UploadActionResult<TableRowWindow>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };
  const at = tablePath(contestId, table);
  if (!at) return { code: "invalid_request" };

  return control(
    async () =>
      tableRowWindowSchema.parse(
        await serverRequest(`${at}/data/window?from=${fromRow}`),
      ),
    [],
  );
}

/** Appends one typed row (`POST .../game/tables/{table}/rows`). */
export async function appendTableRowAction(
  contestId: string,
  table: string,
  values: string[],
): Promise<UploadActionResult<TableData>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };
  const at = tablePath(contestId, table);
  if (!at) return { code: "invalid_request" };

  // The row marks the game out of date, as in `completeTableUploadAction`; the
  // server's row validation names the column at fault.
  return controlNamingFault(
    async () =>
      tableDataSchema.parse(
        await serverRequest(`${at}/rows`, {
          method: "POST",
          body: { values },
        }),
      ),
    changed(contestId),
  );
}

/** Tombstones one row (`DELETE .../game/tables/{table}/rows/{row}`); 204 on success. */
export async function deleteTableRowAction(
  contestId: string,
  table: string,
  row: number,
): Promise<UploadActionResult<never>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };
  const at = tablePath(contestId, table);
  // The row comes from the caller as it was serialised, like every argument.
  if (!at || !Number.isSafeInteger(row) || row < 0) return { code: "invalid_request" };

  // Marks the game out of date, like an added row.
  return serverAction(
    () => serverRequest(`${at}/rows/${row}`, { method: "DELETE" }),
    { revalidate: changed(contestId) },
  );
}

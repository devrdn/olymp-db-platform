"use server";

import { revalidatePath } from "next/cache";

import { ApiError, failureCode } from "@/lib/api/client";
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
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

/**
 * `detail` is `ApiError.message`, shown beside (never instead of) the
 * dictionary sentence for refusals whose sentence is generic because the table,
 * column or row at fault is specific to this attempt.
 */
export type GameState = { code?: string; saved?: boolean; detail?: string };

/** A refusal code, or what the write produced (an upload id, a game's status). */
export type UploadActionResult<T> = { code?: string; value?: T; detail?: string };

/** Turns a caught error into `{code, detail}` for the table-builder actions. */
function refusal(error: unknown): { code: string; detail?: string } {
  return {
    code: failureCode(error),
    detail: error instanceof ApiError ? error.message : undefined,
  };
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

  const failure = await serverRequest(`/contests/${contestId}/game/script`, {
    method: "PUT",
    body: { script },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // The publish gate and this section both depend on whether the game is built.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

/**
 * The build's current state, through a Server Action so the session cookie and
 * the API address stay server-side.
 */
export async function gameStatusAction(contestId: string): Promise<Game | null> {
  if (!isId(contestId)) return null;
  return serverRequest(`/contests/${contestId}/game`).then(
    (payload) => gameSchema.parse(payload),
    () => null,
  );
}

/**
 * Requests a rebuild (`POST .../game/build`), the only way rows typed in the
 * table builder reach a database. Answers 202 with the game.
 */
export async function requestGameBuildAction(contestId: string): Promise<UploadActionResult<Game>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  let value: Game;
  try {
    value = gameSchema.parse(await serverRequest(`/contests/${contestId}/game/build`, { method: "POST" }));
  } catch (error) {
    return refusal(error);
  }

  revalidatePath(`/contests/${contestId}`, "layout");
  return { value };
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

  let value: Upload;
  try {
    const body = await serverRequest(`/contests/${contestId}/game/uploads`, {
      method: "POST",
      body: { filename, declared_bytes: declaredBytes },
    });
    value = uploadSchema.parse(body);
  } catch (error) {
    return { code: failureCode(error) };
  }

  // Another tab or a racing reload must see the "receiving" row.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { value };
}

/**
 * The upload still receiving, if any, so a reloaded page resumes from the
 * server's state rather than the browser's. `null` on any failure: the page
 * already has its initial answer.
 */
export async function currentGameUploadAction(contestId: string): Promise<Upload | null> {
  if (!isId(contestId)) return null;
  return serverRequest(`/contests/${contestId}/game/uploads/current`).then(
    (payload) => uploadSchema.parse(payload),
    () => null,
  );
}

/** A slice of a completed upload's lines (`GET .../uploads/{id}/window`) for the viewer. */
export async function gameUploadWindowAction(
  contestId: string,
  uploadId: string,
  fromLine: number,
): Promise<UploadActionResult<UploadWindow>> {
  if (!isId(contestId) || !isId(uploadId)) return { code: "game_upload_not_found" };

  let value: UploadWindow;
  try {
    const body = await serverRequest(
      `/contests/${contestId}/game/uploads/${uploadId}/window?from=${fromLine}`,
    );
    value = uploadWindowSchema.parse(body);
  } catch (error) {
    return { code: failureCode(error) };
  }

  return { value };
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

  let value: Game;
  try {
    const body = await serverRequest(`/contests/${contestId}/game/uploads/${uploadId}/complete`, {
      method: "POST",
    });
    value = gameSchema.parse(body);
  } catch (error) {
    return { code: failureCode(error) };
  }

  revalidatePath(`/contests/${contestId}`, "layout");

  return { value };
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

  const failure = await serverRequest(`/contests/${contestId}/game/uploads/${uploadId}/abort`, {
    method: "POST",
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // Frees the "one receiving upload" slot for a reload's read.
  revalidatePath(`/contests/${contestId}`, "layout");

  return {};
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

  const failure = await serverRequest(`/contests/${contestId}/game/definition`, {
    method: "PUT",
    body: definition,
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    return {
      code: failureCode(failure),
      // Several validation refusals name the table or column at fault.
      detail: failure instanceof ApiError ? failure.message : undefined,
    };
  }

  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

/**
 * A table's CSV upload still receiving, if any, so a reload resumes from the
 * server's state. `null` on any failure.
 */
export async function currentTableUploadAction(contestId: string, table: string): Promise<TableData | null> {
  if (!isId(contestId)) return null;
  return serverRequest(`/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/current`).then(
    (payload) => tableDataSchema.parse(payload),
    () => null,
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

  let value: TableData;
  try {
    const body = await serverRequest(
      `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data`,
      { method: "POST", body: { declared_bytes: declaredBytes } },
    );
    value = tableDataSchema.parse(body);
  } catch (error) {
    return { code: failureCode(error) };
  }

  return { value };
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

  let value: TableData;
  try {
    const body = await serverRequest(
      `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/${id}/complete`,
      { method: "POST" },
    );
    value = tableDataSchema.parse(body);
  } catch (error) {
    // The server's full validation names the row and column at fault; `detail`
    // carries it.
    return refusal(error);
  }

  // The file marks the built game out of date on the server, and `page.tsx`
  // renders that notice, so its cached output must be refreshed.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { value };
}

/** Cancels a table's CSV upload before it finished (`POST .../data/{id}/abort`). */
export async function abortTableUploadAction(
  contestId: string,
  table: string,
  id: string,
): Promise<UploadActionResult<TableData>> {
  if (!isId(contestId) || !isId(id)) return { code: "game_table_data_not_found" };

  let value: TableData;
  try {
    const body = await serverRequest(
      `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/${id}/abort`,
      { method: "POST" },
    );
    value = tableDataSchema.parse(body);
  } catch (error) {
    return { code: failureCode(error) };
  }

  return { value };
}

/** A page of a table's current rows (`GET .../data/window`), previewed before any build. */
export async function gameTableDataWindowAction(
  contestId: string,
  table: string,
  fromRow: number,
): Promise<UploadActionResult<TableRowWindow>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  let value: TableRowWindow;
  try {
    const body = await serverRequest(
      `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/window?from=${fromRow}`,
    );
    value = tableRowWindowSchema.parse(body);
  } catch (error) {
    return { code: failureCode(error) };
  }

  return { value };
}

/** Appends one typed row (`POST .../game/tables/{table}/rows`). */
export async function appendTableRowAction(
  contestId: string,
  table: string,
  values: string[],
): Promise<UploadActionResult<TableData>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  let value: TableData;
  try {
    const body = await serverRequest(`/contests/${contestId}/game/tables/${encodeURIComponent(table)}/rows`, {
      method: "POST",
      body: { values },
    });
    value = tableDataSchema.parse(body);
  } catch (error) {
    // The server's row validation names the column at fault.
    return refusal(error);
  }

  // The row marks the game out of date; see `completeTableUploadAction`.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { value };
}

/** Tombstones one row (`DELETE .../game/tables/{table}/rows/{row}`); 204 on success. */
export async function deleteTableRowAction(
  contestId: string,
  table: string,
  row: number,
): Promise<UploadActionResult<never>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const failure = await serverRequest(
    `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/rows/${row}`,
    { method: "DELETE" },
  ).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failureCode(failure) };

  // Marks the game out of date, like an added row.
  revalidatePath(`/contests/${contestId}`, "layout");

  return {};
}

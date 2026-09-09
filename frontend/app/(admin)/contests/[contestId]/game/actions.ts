"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
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
 * `detail` is the one field neither `saveGameScriptAction` nor any dump
 * action has ever needed: `ApiError.message` itself, for the handful of
 * table-builder refusals whose *dictionary* sentence is deliberately generic
 * ("the message says which row and column" — `errors.game_table_value_
 * invalid`'s own translated text says so directly) because the specific
 * table, column or row is a fact about *this* attempt, not a sentence any
 * translator could have written in advance. `game-builder.tsx` and
 * `game-builder-table.tsx` are what render it, labelled by `detailLabel`,
 * never in place of the dictionary sentence — only beside it (CLAUDE.md rule
 * 1 is about *codes*; this is the one place an English detail earns its own
 * line, the same way `game.buildError` already does for a build PostgreSQL
 * itself refused).
 */
export type GameState = { code?: string; saved?: boolean; detail?: string };

/**
 * A refusal in the API's own words, or the thing the write actually produced.
 *
 * Shared by every action below that hands something back beyond "it worked":
 * beginning an upload needs its id back before a single chunk can be sent,
 * completing one needs the game's fresh status, and a refusal needs its code
 * either way. `saveGameScriptAction`'s `GameState` is not reused for these —
 * it carries `saved: boolean`, which none of these have a use for. `detail`
 * is `GameState`'s own field, explained above.
 */
export type UploadActionResult<T> = { code?: string; value?: T; detail?: string };

/**
 * `catch (error)` turned into the `{code, detail}` half of a refusal —
 * shared by every table-builder action whose own sentinel can carry a
 * row/column-specific message (`completeTableUploadAction`,
 * `appendTableRowAction`) so the two catch blocks read identically instead
 * of two copies of the same ternary drifting apart.
 */
function refusal(error: unknown): { code: string; detail?: string } {
  return {
    code: error instanceof ApiError ? error.code : "unreachable",
    detail: error instanceof ApiError ? error.message : undefined,
  };
}

/**
 * Storing the SQL a contest's game is built from.
 *
 * Storing, not building. The API answers 202 and a background worker does the
 * rest, because a build creates a database and runs the whole script inside
 * it — seconds at best, and not something to hold a form submission open for.
 * The screen watches the status afterwards.
 */
export async function saveGameScriptAction(_previous: GameState, form: FormData): Promise<GameState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  // Not trimmed. Leading whitespace in somebody's SQL is theirs, and a script
  // that is only whitespace is refused by the server as empty anyway — with a
  // code this screen has a sentence for.
  const script = String(form.get("script") ?? "");

  const failure = await serverRequest(`/contests/${contestId}/game/script`, {
    method: "PUT",
    body: { script },
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  // The overview's publish gate and this section's own note both count on
  // whether the game is built.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

/**
 * The build's current state.
 *
 * A Server Action rather than a fetch from the browser, the same way the
 * participant's query log refreshes itself: the session cookie and the API's
 * address are the server's business, and a second path to the API is a second
 * place for either to be wrong.
 */
export async function gameStatusAction(contestId: string): Promise<Game | null> {
  if (!isId(contestId)) return null;
  return serverRequest(`/contests/${contestId}/game`).then(
    (payload) => gameSchema.parse(payload),
    () => null,
  );
}

/**
 * The second way to build a contest's game: an organiser's own finished
 * dump, sent in pieces, rather than a script typed into `GameEditor`.
 *
 * Only the small control calls live here — beginning, checking on, finishing
 * and cancelling an upload. The bytes themselves never do: a chunk is
 * `PUT .../uploads/{id}/chunk`, sent straight from the browser
 * (`game-upload.tsx`'s own doc explains why a Server Action cannot carry
 * them — its body is capped far below a configurable chunk size, and the API
 * origin the browser needs for that one request is the single thing the rest
 * of this file exists to keep server-side, which is why it is this file's
 * one deliberate exception rather than a precedent for more of them).
 */

/**
 * Reserving a place for a new upload — `POST .../uploads`.
 *
 * `filename` and `declaredBytes` are the browser's own claims about a `File`
 * object, taken on trust the same way `uploadImageAction` takes a picture's
 * bytes on trust: the server checks them again where it matters
 * (`declaredBytes` against what actually lands, in `completeGameUploadAction`).
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
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }

  // A second tab, or a reload racing this one, must see the same "receiving"
  // row rather than a stale "no upload yet".
  revalidatePath(`/contests/${contestId}`, "layout");

  return { value };
}

/**
 * The upload a reloaded page finds still receiving, if any — resuming
 * `game-upload.tsx`'s own state from the server's account of it rather than
 * from whatever the browser remembers, the same rule `PUT .../chunk`'s own
 * response follows for `received_bytes`.
 *
 * `null` on any failure, the same as `gameStatusAction`: an unreachable API
 * here is not news the resume banner has anything useful to say about, and
 * the page's own initial load already asked this question once and would
 * rather show what it has than nothing.
 */
export async function currentGameUploadAction(contestId: string): Promise<Upload | null> {
  if (!isId(contestId)) return null;
  return serverRequest(`/contests/${contestId}/game/uploads/current`).then(
    (payload) => uploadSchema.parse(payload),
    () => null,
  );
}

/**
 * A slice of a completed upload's lines — `GET .../uploads/{id}/window`, the
 * console this screen's viewer pages through. Read-only, and asked for
 * often enough while somebody is scrolling that it stays a small, cheap
 * Server Action rather than growing a route of its own.
 */
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
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }

  return { value };
}

/**
 * Finishing an upload once every byte has arrived — `POST .../complete`.
 *
 * Replaces the contest's game exactly the way `saveGameScriptAction` does
 * (`CompleteUpload`'s own doc: "the same path as `SetScript`"), so it
 * revalidates the same path for the same reason: the overview's publish
 * gate and this workspace's own tab both count on whether the game is
 * built.
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
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }

  revalidatePath(`/contests/${contestId}`, "layout");

  return { value };
}

/**
 * Cancelling an upload still receiving — `POST .../abort`. Never called on
 * one already finished: `game-upload.tsx` only offers this while its own
 * state is "uploading", and the server would refuse it anyway
 * (`game_upload_already_complete`).
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

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  // Frees the "one receiving upload" slot a reload's own read would
  // otherwise still see as taken.
  revalidatePath(`/contests/${contestId}`, "layout");

  return {};
}

/**
 * The third way to build a contest's game: a structural description of its
 * tables and columns rather than SQL — `game-builder.tsx`'s own screen.
 *
 * Only the small control calls live here, the same split `actions.ts`'s own
 * doc draws for the dump's chunks above: a table's CSV bytes are `PUT`
 * straight from the browser (`game-builder-table.tsx`'s own doc gives the
 * identical reasoning `putChunk` already does in `game-upload.tsx` — a
 * chunk's size is this installation's own configured ceiling, read at
 * runtime, and a Server Action's body is capped far below it).
 */

/**
 * Storing the structural description one contest's game is built from —
 * `PUT .../game/definition`. Replaces the contest's game exactly the way
 * `saveGameScriptAction` does (`SetDefinition`'s own doc: "exactly like
 * SetScript"), so it revalidates the same path for the same reason: the
 * overview's publish gate and this workspace's own tabs both count on
 * whether the game is built.
 *
 * The draft travels as one JSON string in a hidden field, not as one form
 * field per table and column: it is a tree the organiser edits in memory,
 * the same reason `GameEditor`'s own mirror textarea carries a whole script
 * as one field rather than one control per line.
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
      code: failure instanceof ApiError ? failure.code : "unreachable",
      // Several of `Definition.Validate`'s own refusals name the table or
      // column at fault (`ErrDefinitionInvalidName`, `ErrDefinitionDuplicate
      // Name`, `ErrDefinitionInvalidType`, `ErrDefinitionInvalidPrimaryKey`
      // — `definition.go`'s own `%w: table %q` formatting) — `GameState.
      // detail`'s own doc explains why that text is worth carrying up
      // alongside the dictionary's generic sentence rather than in place of
      // it.
      detail: failure instanceof ApiError ? failure.message : undefined,
    };
  }

  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

/**
 * The chunked upload a reloaded page finds still receiving for one table,
 * if any — `GET .../game/tables/{table}/data/current`, `currentGameUploadAction`'s
 * own doc mirrored for a table's own CSV rather than a whole dump: resuming
 * `game-builder-table.tsx`'s own state from the server's account of it,
 * never from what the browser happens to remember.
 *
 * `null` on any failure, the identical reasoning `currentGameUploadAction`
 * gives: an unreachable API here is not news the resume banner has
 * anything useful to say about.
 */
export async function currentTableUploadAction(contestId: string, table: string): Promise<TableData | null> {
  if (!isId(contestId)) return null;
  return serverRequest(`/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/current`).then(
    (payload) => tableDataSchema.parse(payload),
    () => null,
  );
}

/**
 * Reserving a place for one table's own chunked CSV upload —
 * `POST .../game/tables/{table}/data`. Paced by the same shared budget
 * `beginGameUploadAction`'s own doc explains (`allowUploadBegin` on the
 * server, shared between a dump and a table's own upload).
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
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }

  return { value };
}

/**
 * Finishing one table's chunked CSV upload — `POST .../data/{id}/complete`.
 *
 * 200 on the wire, not 202: a table's own CSV never builds anything by
 * itself (`tabledata.go`'s own doc — a table's data is loaded the next time
 * the *whole* game builds, not the moment this call returns), so there is no
 * pending build for this screen to watch afterwards the way
 * `completeGameUploadAction`'s own doc explains for a whole dump.
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
    // `CompleteTableUpload`'s own full pass (`validateTableFile`) is where
    // `game_table_header_mismatch`, `_row_field_count`, `_value_invalid`,
    // `_field_too_long`, `_line_too_long` and `_too_many_rows` are actually
    // detected — every one of them naming the row and column at fault in
    // `err.Error()` itself (`tablecsv.go`'s own doc: "without that a refusal
    // on a file of a million rows is useless"). `refusal`'s own `detail` is
    // what carries that text up to the screen.
    return refusal(error);
  }

  return { value };
}

/**
 * Cancelling one table's chunked CSV upload before it finished —
 * `POST .../data/{id}/abort`. Never called on one already finished — the
 * same convention `abortGameUploadAction`'s own doc states for a dump.
 */
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
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }

  return { value };
}

/**
 * A page of one table's current rows — `GET .../data/window`, the console's
 * own preview of a table before it is ever built, the same role
 * `gameUploadWindowAction` plays for a dump's lines.
 */
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
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }

  return { value };
}

/**
 * Adding one row typed into a form directly, rather than uploaded in a file
 * — `POST .../game/tables/{table}/rows`.
 */
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
    // `validateRow` (`tablecsv.go`) is what `AppendTableRow` calls before
    // storing this row, and its own refusal already names the column at
    // fault — `refusal`'s own doc explains why that text travels as
    // `detail`.
    return refusal(error);
  }

  return { value };
}

/**
 * Tombstoning one row of a table's current data —
 * `DELETE .../game/tables/{table}/rows/{row}`. 204 on the wire, so there is
 * nothing to parse on success, the same shape `abortGameUploadAction`'s own
 * sibling calls take for a write that only ever answers success or a named
 * refusal.
 */
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

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  return {};
}

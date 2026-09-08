"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { gameSchema, uploadSchema, uploadWindowSchema, type Game, type Upload, type UploadWindow } from "@/lib/api/game";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type GameState = { code?: string; saved?: boolean };

/**
 * A refusal in the API's own words, or the thing the write actually produced.
 *
 * Shared by every action below that hands something back beyond "it worked":
 * beginning an upload needs its id back before a single chunk can be sent,
 * completing one needs the game's fresh status, and a refusal needs its code
 * either way. `saveGameScriptAction`'s `GameState` is not reused for these —
 * it carries `saved: boolean`, which none of these have a use for.
 */
export type UploadActionResult<T> = { code?: string; value?: T };

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

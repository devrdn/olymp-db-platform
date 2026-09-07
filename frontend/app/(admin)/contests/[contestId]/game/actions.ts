"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { gameSchema, type Game } from "@/lib/api/game";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type GameState = { code?: string; saved?: boolean };

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
 * Removing one participant's database.
 *
 * The destructive half of this screen, and the reason it is a Server Action
 * behind a confirmation rather than a link: the database is dropped with
 * whatever is connected to it forced closed, so a participant mid-query loses
 * that query. Nothing else of theirs moves — their answers, their score and
 * their clock live in the core database and this does not touch them — and
 * their next query or schema load rebuilds the database under the same name.
 *
 * The database name is not checked here the way an identifier would be. It is
 * PostgreSQL's own name rather than a UUID, and the server answers
 * `game_instance_not_found` for anything that is not one of this contest's —
 * which is the same answer it must give for another contest's real database,
 * so guessing at the shape here would buy nothing.
 */
export async function dropGameInstanceAction(
  _previous: GameState,
  form: FormData,
): Promise<GameState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const database = String(form.get("database") ?? "");
  if (database === "") return { code: "game_instance_not_found" };

  const failure = await serverRequest(
    `/contests/${contestId}/game/instances/${encodeURIComponent(database)}`,
    { method: "DELETE" },
  ).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) return { code: failure instanceof ApiError ? failure.code : "unreachable" };

  // The list is read by a server component, so the page has to be asked again
  // for the row to change.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

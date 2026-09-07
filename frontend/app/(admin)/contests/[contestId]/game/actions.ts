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

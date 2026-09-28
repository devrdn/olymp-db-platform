"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type GameState = { code?: string; saved?: boolean };

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

  if (failure) return { code: failureCode(failure) };

  // The list is read by a server component, so the page has to be asked again
  // for the row to change.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

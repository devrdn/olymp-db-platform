"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type GameState = { code?: string; saved?: boolean };

/**
 * Drops one participant's database, forcing connections closed, so a running
 * query is lost. Answers, score and clock live in the core database and are
 * untouched; the next query rebuilds it under the same name. The name is not
 * validated here: the server answers `game_instance_not_found` for anything not
 * this contest's.
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

  // The list is server-rendered, so revalidate.
  revalidatePath(`/contests/${contestId}`, "layout");

  return { saved: true };
}

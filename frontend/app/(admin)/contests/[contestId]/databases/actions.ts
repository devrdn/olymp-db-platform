"use server";

import { isId, pathSegment } from "@/lib/api/ids";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";

import { contestChanged } from "../revalidation";

export type GameState = ActionState;

/**
 * Drops one participant's database, forcing connections closed, so a running
 * query is lost. Answers, score and clock live in the core database and are
 * untouched; the next query rebuilds it under the same name. The name is only
 * kept a single path segment here: the server answers `game_instance_not_found`
 * for anything not this contest's.
 */
export async function dropGameInstanceAction(
  _previous: GameState,
  form: FormData,
): Promise<GameState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  const database = String(form.get("database") ?? "");
  if (database === "") return { code: "game_instance_not_found" };
  const segment = pathSegment(database);
  if (segment === null) return { code: "invalid_request" };

  // The list is server-rendered, so revalidate.
  return serverAction(
    () => serverRequest(`/contests/${contestId}/game/instances/${segment}`, { method: "DELETE" }),
    { revalidate: contestChanged(contestId) },
  );
}

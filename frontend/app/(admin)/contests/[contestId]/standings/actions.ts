"use server";

import { revalidatePath } from "next/cache";

import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type RevealState = { code?: string; revealedAt?: string };

/**
 * Reveals a frozen contest's final standings (POST .../leaderboard/reveal).
 *
 * Irreversible on the server, which is why the button asks first. A second
 * reveal is not an error there: it answers with the moment already in force.
 */
export async function revealStandingsAction(_previous: RevealState, form: FormData): Promise<RevealState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  try {
    const payload = (await serverRequest(`/contests/${contestId}/leaderboard/reveal`, { method: "POST" })) as {
      revealed_at?: string;
    };
    revalidatePath(`/contests/${contestId}`, "layout");
    return { revealedAt: payload?.revealed_at };
  } catch (error: unknown) {
    return { code: error instanceof ApiError ? error.code : "unreachable" };
  }
}

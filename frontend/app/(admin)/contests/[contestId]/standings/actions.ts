"use server";

import { revalidatePath } from "next/cache";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { serverRequest } from "@/lib/api/server";

export type RevealState = { code?: string; revealedAt?: string };

/**
 * Reveals a frozen contest's final standings (POST .../leaderboard/reveal).
 * Irreversible; a second reveal answers with the moment already set.
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
    return { code: failureCode(error) };
  }
}

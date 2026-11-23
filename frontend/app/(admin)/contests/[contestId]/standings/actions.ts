"use server";

import { isId } from "@/lib/api/ids";
import { serverAction, serverRequest, type ActionState } from "@/lib/api/server";

export type RevealState = ActionState & { revealedAt?: string };

/**
 * Reveals a frozen contest's final standings (POST .../leaderboard/reveal).
 * Irreversible; a second reveal answers with the moment already set.
 */
export async function revealStandingsAction(_previous: RevealState, form: FormData): Promise<RevealState> {
  const contestId = form.get("contestId");
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  return serverAction(
    async () =>
      (await serverRequest(`/contests/${contestId}/leaderboard/reveal`, { method: "POST" })) as {
        revealed_at?: string;
      },
    {
      revalidate: [{ layout: `/contests/${contestId}` }],
      success: (answer) => ({ ok: true, revealedAt: answer?.revealed_at }),
    },
  );
}

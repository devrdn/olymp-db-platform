"use server";

import { revalidatePath } from "next/cache";

import { ApiError, failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { staffStandingsSchema, type StaffStandings } from "@/lib/api/leaderboard";
import { serverRequest } from "@/lib/api/server";

export type RevealState = { code?: string; revealedAt?: string };

export type StaffStandingsResult = { kind: "ok"; standings: StaffStandings } | { kind: "refused"; code: string };

/**
 * Reads the live table (GET /contests/{id}/leaderboard/live) for the staff
 * poll; a plain fetch, since `router.refresh()` would re-run the whole layout's
 * reads.
 */
export async function fetchStaffStandingsAction(contestId: string): Promise<StaffStandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const payload = await serverRequest(`/contests/${contestId}/leaderboard/live`);
    return { kind: "ok", standings: staffStandingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

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

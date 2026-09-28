"use server";

import { revalidatePath } from "next/cache";

import { ApiError, failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { staffStandingsSchema, type StaffStandings } from "@/lib/api/leaderboard";
import { serverRequest } from "@/lib/api/server";

export type RevealState = { code?: string; revealedAt?: string };

export type StaffStandingsResult = { kind: "ok"; standings: StaffStandings } | { kind: "refused"; code: string };

/**
 * Reads a contest's live table (GET /contests/{id}/leaderboard/live) for the
 * staff page that polls it.
 *
 * A plain data fetch rather than router.refresh(): the staff table is the
 * one thing on the page that moves while a contest runs, and re-running the
 * whole layout tree for it would re-fetch the contest, the publish check and
 * the questions list along with it, on every poll, for no reason the table
 * needs.
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
    return { code: failureCode(error) };
  }
}

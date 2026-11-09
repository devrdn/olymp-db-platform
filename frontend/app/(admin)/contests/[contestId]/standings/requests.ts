import { ApiError, request } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { staffStandingsSchema, type StaffStandings } from "@/lib/api/leaderboard";

export type StaffStandingsResult = { kind: "ok"; standings: StaffStandings } | { kind: "refused"; code: string };

/**
 * Reads the live table (GET /contests/{id}/leaderboard/live) for the staff
 * poll, from the browser with the session cookie, so the table never passes
 * through the Next process. Never throws: any failure is a refused read.
 */
export async function fetchStaffStandings(contestId: string): Promise<StaffStandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const payload = await request(`/contests/${contestId}/leaderboard/live`, { credentials: "same-origin" });
    return { kind: "ok", standings: staffStandingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

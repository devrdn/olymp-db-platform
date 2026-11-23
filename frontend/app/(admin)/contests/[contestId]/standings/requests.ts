import { browserCall, browserRequest, type Refused } from "@/lib/api/browser";
import { isId } from "@/lib/api/ids";
import { staffStandingsSchema, type StaffStandings } from "@/lib/api/leaderboard";

export type StaffStandingsResult = { kind: "ok"; standings: StaffStandings } | Refused;

/**
 * Reads the live table (GET /contests/{id}/leaderboard/live) for the staff
 * poll, from the browser with the session cookie, so the table never passes
 * through the Next process. Never throws: any failure is a refused read.
 */
export async function fetchStaffStandings(contestId: string): Promise<StaffStandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  return browserCall(async () => ({
    kind: "ok" as const,
    standings: staffStandingsSchema.parse(await browserRequest(`/contests/${contestId}/leaderboard/live`)),
  }));
}

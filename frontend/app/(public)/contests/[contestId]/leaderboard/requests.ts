import type { StandingsResult } from "@/components/product/use-standings";
import { ApiError, request } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { standingsSchema } from "@/lib/api/leaderboard";
import type { Locale } from "@/lib/i18n/config";

/**
 * Reads the public table (GET /contests/{id}/leaderboard) for polling, from the
 * browser rather than through a server action, so an audience's polls never
 * pass through the Next process; the ingress hands the API the visitor's own
 * address for its per-address limit.
 */
export async function fetchPublicStandings(contestId: string, locale: Locale): Promise<StandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const payload = await request(`/contests/${contestId}/leaderboard?lang=${locale}`, { credentials: "same-origin" });
    return { kind: "ok", standings: standingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

"use server";

import type { StandingsResult } from "@/components/product/use-standings";
import { ApiError } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { standingsSchema } from "@/lib/api/leaderboard";
import { serverRequest } from "@/lib/api/server";
import { activeLocale } from "@/lib/i18n/server";

/**
 * Reads a contest's public table (GET /contests/{id}/leaderboard) for the page
 * that polls it. Open to a visitor with no session, exactly as the endpoint
 * is; the visitor's own address is forwarded, so the API's per-address limit
 * falls on them and not on this server.
 */
export async function fetchPublicStandingsAction(contestId: string): Promise<StandingsResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const locale = await activeLocale();
    const payload = await serverRequest(`/contests/${contestId}/leaderboard?lang=${locale}`);
    return { kind: "ok", standings: standingsSchema.parse(payload) };
  } catch (error: unknown) {
    if (error instanceof ApiError) return { kind: "refused", code: error.code };
    return { kind: "refused", code: "unreachable" };
  }
}

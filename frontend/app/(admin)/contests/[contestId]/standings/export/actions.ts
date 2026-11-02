"use server";

import { failureCode } from "@/lib/api/client";
import { isId } from "@/lib/api/ids";
import { resultsReportSchema, type ResultsReport } from "@/lib/api/results";
import { serverRequest } from "@/lib/api/server";

export type ResultsPreviewResult = { kind: "ok"; report: ResultsReport } | { kind: "refused"; code: string };

/**
 * Reads the results preview (GET /contests/{id}/results): every registration
 * with the place the export gives it, and whether email may be exported.
 */
export async function fetchResultsAction(contestId: string): Promise<ResultsPreviewResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const payload = await serverRequest(`/contests/${contestId}/results`);
    return { kind: "ok", report: resultsReportSchema.parse(payload) };
  } catch (error: unknown) {
    return { kind: "refused", code: failureCode(error) };
  }
}

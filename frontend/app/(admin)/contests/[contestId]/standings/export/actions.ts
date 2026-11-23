"use server";

import { isId } from "@/lib/api/ids";
import { resultsReportSchema, type ResultsReport } from "@/lib/api/results";
import { serverAction, serverRequest } from "@/lib/api/server";

/** Shaped like the export's own browser reads (`./requests`), which the same dialog renders. */
export type ResultsPreviewResult = { kind: "ok"; report: ResultsReport } | { kind: "refused"; code: string };

/**
 * Reads the results preview (GET /contests/{id}/results): every registration
 * with the place the export gives it, and whether email may be exported.
 */
export async function fetchResultsAction(contestId: string): Promise<ResultsPreviewResult> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  return serverAction(async () => resultsReportSchema.parse(await serverRequest(`/contests/${contestId}/results`)), {
    success: (report) => ({ kind: "ok", report }),
    failure: ({ code }) => ({ kind: "refused", code }),
  });
}

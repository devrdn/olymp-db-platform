import { browserCall, browserDownload, browserRequest, type Refused } from "@/lib/api/browser";
import { isId } from "@/lib/api/ids";
import { resultsReportSchema, type ResultsExportBody, type ResultsReport } from "@/lib/api/results";

import { resultsFileName } from "./request";

/**
 * The export itself, sent from the browser with the session cookie: a file
 * of up to 2000 rows goes straight to the browser rather than through the
 * Next process, as the play screen's requests do. Each function returns an
 * outcome rather than throwing, so the form shows the code with
 * `messageForCode`.
 */

export type DownloadOutcome = { kind: "ok"; blob: Blob; fileName: string } | Refused;

export type ReportOutcome = { kind: "ok"; report: ResultsReport } | Refused;

/** The CSV as the API writes it, under the name its `Content-Disposition` gives. */
export async function downloadResultsCsv(contestId: string, body: ResultsExportBody): Promise<DownloadOutcome> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  return browserCall(async () => {
    const { blob, fileName } = await browserDownload(`/contests/${contestId}/results/export`, { method: "POST", body });
    return { kind: "ok" as const, blob, fileName: fileName ?? resultsFileName("csv", contestId, new Date().toISOString()) };
  });
}

/** The same report as JSON, which the browser draws as a PDF. */
export async function fetchResultsReport(contestId: string, body: ResultsExportBody): Promise<ReportOutcome> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  return browserCall(async () => ({
    kind: "ok" as const,
    report: resultsReportSchema.parse(await browserRequest(`/contests/${contestId}/results/export`, { method: "POST", body })),
  }));
}

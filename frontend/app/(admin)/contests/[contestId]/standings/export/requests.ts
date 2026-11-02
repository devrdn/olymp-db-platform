import { failureCode, request, requestDownload, type RequestOptions } from "@/lib/api/client";
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

export type DownloadOutcome = { kind: "ok"; blob: Blob; fileName: string } | { kind: "refused"; code: string };

export type ReportOutcome = { kind: "ok"; report: ResultsReport } | { kind: "refused"; code: string };

const browserOptions = (body: ResultsExportBody): RequestOptions => ({
  method: "POST",
  body,
  credentials: "same-origin",
});

/** The CSV as the API writes it, under the name its `Content-Disposition` gives. */
export async function downloadResultsCsv(contestId: string, body: ResultsExportBody): Promise<DownloadOutcome> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const { blob, fileName } = await requestDownload(`/contests/${contestId}/results/export`, browserOptions(body));
    return { kind: "ok", blob, fileName: fileName ?? resultsFileName("csv", contestId, new Date().toISOString()) };
  } catch (error: unknown) {
    return { kind: "refused", code: failureCode(error) };
  }
}

/** The same report as JSON, which the browser draws as a PDF. */
export async function fetchResultsReport(contestId: string, body: ResultsExportBody): Promise<ReportOutcome> {
  if (!isId(contestId)) return { kind: "refused", code: "not_found" };
  try {
    const payload = await request(`/contests/${contestId}/results/export`, browserOptions(body));
    return { kind: "ok", report: resultsReportSchema.parse(payload) };
  } catch (error: unknown) {
    return { kind: "refused", code: failureCode(error) };
  }
}

import {
  RESULTS_FIELDS,
  type ReportStatus,
  type ResultsDetail,
  type ResultsExportBody,
  type ResultsField,
} from "@/lib/api/results";

export type ExportKind = "csv" | "table" | "protocol";

export type ExportOptions = {
  kind: ExportKind;
  detail: ResultsDetail;
  fields: ReadonlySet<ResultsField>;
  excel: boolean;
};

/** The PDFs are drawn from the JSON report; only the CSV is built by the API. */
export function exportBody(options: ExportOptions, registrations: string[]): ResultsExportBody {
  const body: ResultsExportBody = {
    format: options.kind === "csv" ? "csv" : "json",
    registrations,
    detail: options.detail,
    fields: RESULTS_FIELDS.filter((field) => options.fields.has(field)),
  };
  if (options.kind === "csv") body.csv = { excel: options.excel };
  return body;
}

/** A protocol is a signed-off record, so a report still moving does not offer one. */
export function protocolAvailable(status: ReportStatus): boolean {
  return status === "final" || status === "archived";
}

/** The CSV name matches the API's `Content-Disposition`; the stamp is the report's own time in UTC. */
export function resultsFileName(kind: ExportKind, contestId: string, generatedAt: string): string {
  const stamp = new Date(generatedAt)
    .toISOString()
    .replace(/\.\d{3}Z$/, "Z")
    .replace(/[-:]/g, "");
  switch (kind) {
    case "csv":
      return `results-${contestId}-${stamp}.csv`;
    case "table":
      return `results-${contestId}-${stamp}.pdf`;
    case "protocol":
      return `protocol-${contestId}-${stamp}.pdf`;
  }
}

import { renderPdf, type PdfOptions } from "@/lib/pdf/render";

import {
  contestTitle,
  measureOf,
  resultsProtocolDocument,
  resultsTableDocument,
  type ResultsPdfInput,
} from "./results-document";

export type ResultsPdfRequest = Omit<ResultsPdfInput, "logo" | "headerTitle"> & {
  /** The installation's uploaded logo; none means the product mark. */
  logoHref?: string;
};

/**
 * The results table or the results protocol as a PDF file, made in the
 * browser; the caller imports this module on demand, so the library is part of
 * no page's own code. A logo that cannot be drawn is left out; a protocol of a
 * preliminary report rejects before anything is loaded.
 */
export async function makeResultsPdf(kind: "table" | "protocol", request: ResultsPdfRequest, options?: PdfOptions): Promise<Blob> {
  const { logoHref, ...input } = request;
  const document = kind === "table" ? resultsTableDocument : resultsProtocolDocument;
  // Throws on a report this kind refuses, before anything is loaded.
  document({ ...input, logo: null });

  return renderPdf(
    { title: contestTitle(input.report, input.language), logoHref, width: measureOf(input.report) },
    ({ logo, headerTitle }) => document({ ...input, logo, headerTitle }),
    options,
  );
}

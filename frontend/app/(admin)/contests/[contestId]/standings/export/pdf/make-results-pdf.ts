import { fetchFont } from "../../../story/pdf/fonts";
import { loadLogo } from "../../../story/pdf/logo";
import { fitTitle, preparedLibrary, type StoryPdfOptions } from "../../../story/pdf/make-story-pdf";
import { titleSpace } from "../../../story/pdf/story-document";
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
 * browser with the story PDF's library and faces; the caller imports this
 * module on demand, so neither is part of a page's own code. A logo that
 * cannot be drawn is left out; a protocol of a preliminary report rejects
 * before anything is loaded.
 */
export async function makeResultsPdf(
  kind: "table" | "protocol",
  request: ResultsPdfRequest,
  { fontSource = "/fonts/pdf", readFont = fetchFont, drawLogo = loadLogo }: StoryPdfOptions = {},
): Promise<Blob> {
  const { logoHref, ...input } = request;
  const document = kind === "table" ? resultsTableDocument : resultsProtocolDocument;
  // Throws on a report this kind refuses.
  document({ ...input, logo: null });

  const [pdfMake, logo] = await Promise.all([
    preparedLibrary(fontSource, readFont),
    logoHref ? drawLogo(logoHref).catch(() => null) : Promise.resolve("mark" as const),
  ]);
  const headerTitle = await fitTitle(
    pdfMake,
    contestTitle(input.report, input.language),
    titleSpace(logo, measureOf(input.report)),
  );
  const bytes = await pdfMake.createPdf(document({ ...input, logo, headerTitle })).getBuffer();
  return new Blob([bytes as Uint8Array<ArrayBuffer>], { type: "application/pdf" });
}

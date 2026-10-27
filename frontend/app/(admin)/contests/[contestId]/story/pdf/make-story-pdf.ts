import type { PdfMake } from "pdfmake";

import { FAMILIES, fetchFont, loadFonts, type ReadFont } from "./fonts";
import { loadLogo, type LogoImage } from "./logo";
import { fitColumns, type TableRef } from "./story-markdown";
import { storyDocument } from "./story-document";

export type StoryPdfRequest = {
  title: string;
  /** The story's language code. */
  language: string;
  /** The saved Markdown of that language. */
  markdown: string;
  /** The installation's uploaded logo; none means the product mark. */
  logoHref?: string;
};

export type StoryPdfOptions = {
  /** Where the faces are read from: a URL path in the browser, a directory in a test. */
  fontSource?: string;
  readFont?: ReadFont;
  /** Draws the uploaded logo; a test replaces the browser's canvas. */
  drawLogo?: (href: string) => Promise<LogoImage>;
};

/**
 * The PDF library with the faces installed. It is imported here, on demand, so
 * it is part of no page's own code.
 */
export async function preparedLibrary(fontSource: string, readFont: ReadFont): Promise<PdfMake> {
  const [loaded, fonts] = await Promise.all([import("pdfmake"), loadFonts(fontSource, readFont)]);
  const pdfMake = ((loaded as { default?: PdfMake }).default ?? loaded) as PdfMake;
  // Everything the file holds is handed over in memory; the library is never to fetch or open anything.
  pdfMake.setUrlAccessPolicy(() => false);
  pdfMake.setLocalAccessPolicy?.(() => false);
  for (const [file, bytes] of fonts) pdfMake.virtualfs.writeFileSync(file, bytes);
  // A copy: the library rewrites the table it is given.
  pdfMake.setFonts(structuredClone(FAMILIES));
  return pdfMake;
}

type Measured = { _minWidth: number; _maxWidth: number };

/**
 * Each table's column widths as the screen's table layout gives them. The
 * library measures a column's narrowest and widest content while it lays a
 * table out, and leaves the measurements on the table's own `widths`; a
 * layout of the tables alone reads them.
 */
export async function measureColumns(pdfMake: PdfMake, tables: TableRef[]): Promise<number[][]> {
  if (tables.length === 0) return [];
  await pdfMake.createPdf({ pageSize: "A4", defaultStyle: { font: "Onest" }, content: tables.map((table) => table.node) }).getBuffer();
  return tables.map(({ node, width }) => {
    const measured = (node.table as { widths: Measured[] }).widths;
    return fitColumns(
      measured.map((column) => column._minWidth),
      measured.map((column) => column._maxWidth),
      width,
    );
  });
}

/**
 * The story as a PDF file. A logo that cannot be loaded or drawn is left out
 * rather than stopping the download. Anything else, such as a face that fails
 * to load, rejects, and the caller says so; the next call loads it again.
 */
export async function makeStoryPdf(
  request: StoryPdfRequest,
  { fontSource = "/fonts/pdf", readFont = fetchFont, drawLogo = loadLogo }: StoryPdfOptions = {},
): Promise<Blob> {
  const [pdfMake, logo] = await Promise.all([
    preparedLibrary(fontSource, readFont),
    request.logoHref ? drawLogo(request.logoHref).catch(() => null) : Promise.resolve("mark" as const),
  ]);

  const input = { title: request.title, language: request.language, markdown: request.markdown, logo };
  const columnWidths = await measureColumns(pdfMake, storyDocument(input).tables);
  const { definition } = storyDocument({ ...input, columnWidths });
  const bytes = await pdfMake.createPdf(definition).getBuffer();
  return new Blob([bytes as Uint8Array<ArrayBuffer>], { type: "application/pdf" });
}

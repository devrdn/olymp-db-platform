import type { PdfMake } from "pdfmake";

import { renderPdf, type PdfOptions } from "@/lib/pdf/render";
import type { Measured } from "@/lib/pdf/types";

import { storyDocument } from "./story-document";
import { fitColumns, type TableRef } from "./story-markdown";

export type StoryPdfRequest = {
  title: string;
  /** The story's language code. */
  language: string;
  /** The saved Markdown of that language. */
  markdown: string;
  /** The installation's uploaded logo; none means the product mark. */
  logoHref?: string;
};

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

/** The story as a PDF file. */
export async function makeStoryPdf(request: StoryPdfRequest, options?: PdfOptions): Promise<Blob> {
  return renderPdf({ title: request.title, logoHref: request.logoHref }, async ({ pdfMake, logo, headerTitle }) => {
    const input = { title: request.title, language: request.language, markdown: request.markdown, logo };
    const columnWidths = await measureColumns(pdfMake, storyDocument(input).tables);
    return storyDocument({ ...input, columnWidths, headerTitle }).definition;
  }, options);
}

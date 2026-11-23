import type { PdfMake } from "pdfmake";

import { HEADER_TITLE } from "./running-header";
import type { Measured } from "./types";

/** How wide `text` sets in the header's title, as the library measures it. */
export async function titleWidth(pdfMake: PdfMake, text: string): Promise<number> {
  const node = { ...HEADER_TITLE, text };
  await pdfMake.createPdf({ pageSize: "A4", content: [node] }).getBuffer();
  return (node as typeof node & Measured)._maxWidth;
}

/**
 * Points a fitted title keeps clear of its space: the library wraps against
 * the width its star column works out, which can come out a rounding error
 * narrower than the space, and a wrapped title would push into the text.
 */
export const TITLE_SLACK = 0.5;

/**
 * The title as the header prints it, on one line: its whitespace collapsed,
 * whole when it fits `width`, else the longest start of it that fits followed
 * by an ellipsis. It is cut between graphemes, so a letter keeps its accent.
 */
export async function fitTitle(pdfMake: PdfMake, title: string, width: number): Promise<string> {
  const line = title.replace(/\s+/g, " ").trim();
  const limit = width - TITLE_SLACK;
  if ((await titleWidth(pdfMake, line)) <= limit) return line;
  const graphemes = Array.from(new Intl.Segmenter().segment(line), ({ segment }) => segment);
  const cut = (count: number) => `${graphemes.slice(0, count).join("").trimEnd()}…`;
  // A binary search: `cut(fits)` fits, or is the bare ellipsis; `cut(over)` does not fit.
  let fits = 0;
  let over = graphemes.length;
  while (over - fits > 1) {
    const middle = Math.floor((fits + over) / 2);
    if ((await titleWidth(pdfMake, cut(middle))) <= limit) fits = middle;
    else over = middle;
  }
  return cut(fits);
}

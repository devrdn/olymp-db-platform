import type { PdfMake } from "pdfmake";

import { fetchFont, type ReadFont } from "./fonts";
import { preparedLibrary } from "./library";
import { loadLogo, type LogoImage } from "./logo";
import { titleSpace, type Logo } from "./running-header";
import { fitTitle } from "./title";
import type { Node } from "./types";

export type PdfOptions = {
  /** Where the faces are read from: a URL path in the browser, a directory in a test. */
  fontSource?: string;
  readFont?: ReadFont;
  /** Draws the uploaded logo; a test replaces the browser's canvas. */
  drawLogo?: (href: string) => Promise<LogoImage>;
};

export type PdfFrame = {
  /** The title the running header prints, cut to fit one line beside the logo. */
  title: string;
  /** The installation's uploaded logo; none means the product mark. */
  logoHref?: string;
  /** The text column's width, which the header spans; the A4 measure when absent. */
  width?: number;
};

/** What a document is built from: the loaded library, the logo as drawn, and the fitted header title. */
export type PdfBuild = { pdfMake: PdfMake; logo: Logo; headerTitle: string };

/**
 * A PDF file: loads the library and the faces, draws the logo, fits the header
 * title, and lets `build` make the document definition from them. A logo that
 * cannot be drawn is left out rather than stopping the download; anything else,
 * such as a face that fails to load, rejects, and the next call loads it again.
 * The library is imported on demand, so it is part of no page's own code.
 */
export async function renderPdf(
  { title, logoHref, width }: PdfFrame,
  build: (loaded: PdfBuild) => Node | Promise<Node>,
  { fontSource = "/fonts/pdf", readFont = fetchFont, drawLogo = loadLogo }: PdfOptions = {},
): Promise<Blob> {
  const [pdfMake, logo] = await Promise.all([
    preparedLibrary(fontSource, readFont),
    logoHref ? drawLogo(logoHref).catch(() => null) : Promise.resolve("mark" as const),
  ]);
  const headerTitle = await fitTitle(pdfMake, title, titleSpace(logo, width));
  const definition = await build({ pdfMake, logo, headerTitle });
  const bytes = await pdfMake.createPdf(definition).getBuffer();
  return new Blob([bytes as Uint8Array<ArrayBuffer>], { type: "application/pdf" });
}

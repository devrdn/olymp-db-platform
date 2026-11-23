import type { PdfMake } from "pdfmake";

import { FAMILIES, loadFonts, type ReadFont } from "./fonts";

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

import type { Face } from "./theme";

/**
 * The faces the PDF embeds: whole static files, one per face, in
 * `public/fonts/pdf/` beside each family's licence. The interface's own fonts
 * are variable files split by script, and a PDF needs one file covering Latin,
 * Romanian and Cyrillic at once.
 *
 * The library knows four styles of a family. Literata's bold is its semibold,
 * the weight `StoryText` sets strong text in; Onest's is its medium, the
 * weight of the story's headings. Neither Onest nor the mono face has an
 * italic, so asking for one gives the upright face rather than a stranger.
 */
export const FAMILIES: Record<Face, { normal: string; bold: string; italics: string; bolditalics: string }> = {
  Literata: {
    normal: "Literata-Regular.ttf",
    bold: "Literata-SemiBold.ttf",
    italics: "Literata-Italic.ttf",
    bolditalics: "Literata-SemiBoldItalic.ttf",
  },
  Onest: {
    normal: "Onest-Regular.ttf",
    bold: "Onest-Medium.ttf",
    italics: "Onest-Regular.ttf",
    bolditalics: "Onest-Medium.ttf",
  },
  JetBrainsMono: {
    normal: "JetBrainsMono-Regular.ttf",
    bold: "JetBrainsMono-Regular.ttf",
    italics: "JetBrainsMono-Regular.ttf",
    bolditalics: "JetBrainsMono-Regular.ttf",
  },
};

export const FONT_FILES = [...new Set(Object.values(FAMILIES).flatMap((family) => Object.values(family)))];

/** Reads one face's bytes from `source`, a URL path in the browser or a directory in a test. */
export type ReadFont = (source: string, file: string) => Promise<Uint8Array>;

export const fetchFont: ReadFont = async (source, file) => {
  const response = await fetch(`${source}/${file}`);
  if (!response.ok) throw new Error(`${file}: ${response.status}`);
  return new Uint8Array(await response.arrayBuffer());
};

const loads = new Map<string, Promise<Map<string, Uint8Array>>>();

/**
 * Every face, read once per source. A failed read is not kept: the next call
 * reads again, so trying again after a lost connection can succeed.
 */
export function loadFonts(source: string, read: ReadFont = fetchFont): Promise<Map<string, Uint8Array>> {
  let load = loads.get(source);
  if (!load) {
    load = Promise.all(FONT_FILES.map(async (file) => [file, await read(source, file)] as const)).then(
      (entries) => new Map(entries),
    );
    loads.set(source, load);
    load.catch(() => loads.delete(source));
  }
  return load;
}

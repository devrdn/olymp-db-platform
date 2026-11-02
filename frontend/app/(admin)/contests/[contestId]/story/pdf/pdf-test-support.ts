/** Test helpers for reading a PDF the library made: its faces, and the letters it could not draw. */

import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { inflateSync } from "node:zlib";

import type { ReadFont } from "./fonts";

export const readFromDisk: ReadFont = async (source, file) => new Uint8Array(await readFile(join(source, file)));

/** The faces a PDF embeds, without their subset tag: `/BaseFont /ABCDEF+Literata-Regular`. */
export function embeddedFaces(pdf: Buffer): string[] {
  const names = [...pdf.toString("latin1").matchAll(/\/BaseFont \/[A-Z]{6}\+([^\s/>[\]]+)/g)].map((match) => match[1]);
  return [...new Set(names)].sort();
}

/** Every face named in the file, embedded or not: a standard face would appear here without a subset tag. */
export function namedFaces(pdf: Buffer): string[] {
  return [...new Set([...pdf.toString("latin1").matchAll(/\/BaseFont \/([^\s/>[\]]+)/g)].map((match) => match[1]))];
}

/**
 * How many glyphs the pages draw with glyph 0, a face's missing-glyph box. The
 * library takes no other face when a letter is missing: it draws that box, so
 * a missing letter shows only here.
 */
export function missingGlyphs(pdf: Buffer): number {
  const text = pdf.toString("latin1");
  let count = 0;
  for (const match of text.matchAll(/stream\r?\n/g)) {
    const start = match.index + match[0].length;
    const end = text.indexOf("endstream", start);
    let content: string;
    try {
      content = inflateSync(pdf.subarray(start, end)).toString("latin1");
    } catch {
      continue;
    }
    for (const shown of content.matchAll(/\[([^\]]*)\]\s*TJ/g)) {
      for (const hex of shown[1].matchAll(/<([0-9a-fA-F]+)>/g)) {
        count += (hex[1].match(/.{4}/g) ?? []).filter((glyph) => glyph === "0000").length;
      }
    }
  }
  return count;
}

// @vitest-environment node
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { inflateSync } from "node:zlib";
import { describe, expect, test, vi } from "vitest";

import type { ReadFont } from "./fonts";
import { TITLE_SLACK, fitTitle, makeStoryPdf, measureColumns, preparedLibrary, titleWidth, type StoryPdfOptions } from "./make-story-pdf";
import { titleSpace } from "./story-document";
import { storyBlocks, type Node } from "./story-markdown";
import { A4, MEASURE } from "./theme";

const FONTS = join(process.cwd(), "public/fonts/pdf");
const readFromDisk: ReadFont = async (source, file) => new Uint8Array(await readFile(join(source, file)));

/** A 2 x 2 JPEG, standing in for what the browser's canvas produces. */
const PIXEL =
  "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAMCAgMCAgMDAwMEAwMEBQgFBQQEBQoHBwYIDAoMDAsKCwsNDhIQDQ4RDgsLEBYQERMUFRUVDA8XGBYUGBIUFRT/2wBDAQMEBAUEBQkFBQkUDQsNFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBT/wAARCAACAAIDAREAAhEBAxEB/8QAFAABAAAAAAAAAAAAAAAAAAAACP/EABQQAQAAAAAAAAAAAAAAAAAAAAD/xAAVAQEBAAAAAAAAAAAAAAAAAAAFB//EABQRAQAAAAAAAAAAAAAAAAAAAAD/2gAMAwEAAhEDEQA/ACOTSp//2Q==";

/**
 * Every face the story uses, in Russian and Romanian. The Romanian letters are
 * the comma-below forms (U+0219, U+021B) the language's standard requires, not
 * the cedilla forms (U+015F, U+0163) many fonts and keyboards substitute.
 */
const STORY = [
  "# Убийство в оранжерее",
  "Дверь была заперта изнутри. **Свидетель** говорит, что *никого не видел*, а ***садовник*** молчит.",
  "## Ștefan și țesătorul…",
  "Au intrat în seră: într-o zi, în România, la Brașov. Au găsit-o încuiată din interior.",
  "```sql\n-- проверка: Ștefan\nSELECT имя FROM свидетели WHERE город = 'Brașov';\n```",
  "| № | Имя | Oraș |\n|--:|-----|------|\n| 1 | Ștefan… | Brașov |",
].join("\n\n");

const request = { title: "Убийство в оранжерее", language: "ru", markdown: STORY };

const options = (overrides: Partial<StoryPdfOptions> = {}): StoryPdfOptions => ({
  fontSource: FONTS,
  readFont: readFromDisk,
  drawLogo: async () => Promise.reject(new Error("no canvas in a test")),
  ...overrides,
});

async function bytes(blob: Blob): Promise<Buffer> {
  return Buffer.from(await blob.arrayBuffer());
}

/** The faces a PDF embeds, without their subset tag: `/BaseFont /ABCDEF+Literata-Regular`. */
function embeddedFaces(pdf: Buffer): string[] {
  const names = [...pdf.toString("latin1").matchAll(/\/BaseFont \/[A-Z]{6}\+([^\s/>[\]]+)/g)].map((match) => match[1]);
  return [...new Set(names)].sort();
}

/** Every face named in the file, embedded or not: a standard face would appear here without a subset tag. */
function namedFaces(pdf: Buffer): string[] {
  return [...new Set([...pdf.toString("latin1").matchAll(/\/BaseFont \/([^\s/>[\]]+)/g)].map((match) => match[1]))];
}

/**
 * How many glyphs the pages draw with glyph 0, a face's missing-glyph box. The
 * library takes no other face when a letter is missing: it draws that box, so
 * a missing letter shows only here.
 */
function missingGlyphs(pdf: Buffer): number {
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

describe("the story PDF's faces", () => {
  test("the fixture uses the comma-below forms and no cedilla ones, and the ellipsis a cut title ends with", () => {
    for (const letter of ["ș", "ț", "…"]) expect(STORY).toContain(letter);
    for (const letter of ["ş", "ţ", "Ş", "Ţ"]) expect(STORY).not.toContain(letter);
  });

  test("Russian and Romanian text embeds exactly the seven faces, names no other, and draws no missing glyph", async () => {
    const pdf = await bytes(await makeStoryPdf(request, options()));

    expect(pdf.subarray(0, 5).toString("latin1")).toBe("%PDF-");
    expect(embeddedFaces(pdf)).toEqual([
      "JetBrainsMono-Regular",
      "Literata-Italic",
      "Literata-Regular",
      "Literata-SemiBold",
      "Literata-SemiBoldItalic",
      "Onest-Medium",
      "Onest-Regular",
    ]);
    expect(namedFaces(pdf)).toHaveLength(7);
    expect(missingGlyphs(pdf)).toBe(0);
  });

  test("the check fails for a face without Cyrillic: the letters it lacks are drawn as its missing-glyph box", async () => {
    // A real TrueType face with Latin and neither Cyrillic nor ș: KaTeX's, which the editor's packages bring.
    const latin = join(process.cwd(), "node_modules/katex/dist/fonts/KaTeX_Main-Regular.ttf");
    const readFont: ReadFont = async (source, file) =>
      file === "Literata-Regular.ttf" ? new Uint8Array(await readFile(latin)) : readFromDisk(FONTS, file);

    const pdf = await bytes(await makeStoryPdf(request, options({ fontSource: "latin-only", readFont })));

    expect(missingGlyphs(pdf)).toBeGreaterThan(0);
  });
});

describe("makeStoryPdf", () => {
  test("makes a PDF file with the uploaded logo, drawn", async () => {
    const drawLogo = vi.fn(async () => ({ src: PIXEL, width: 200, height: 60 }));

    const blob = await makeStoryPdf({ ...request, logoHref: "/api/v1/settings/images/logo?v=abc" }, options({ drawLogo }));

    expect(drawLogo).toHaveBeenCalledWith("/api/v1/settings/images/logo?v=abc");
    expect(blob.type).toBe("application/pdf");
    expect((await bytes(blob)).toString("latin1")).toMatch(/\/Subtype\s*\/Image/);
  });

  test("with no uploaded logo it draws the product mark itself, as shapes, and loads nothing", async () => {
    const drawLogo = vi.fn();

    const pdf = (await bytes(await makeStoryPdf(request, options({ drawLogo })))).toString("latin1");

    expect(drawLogo).not.toHaveBeenCalled();
    expect(pdf).not.toMatch(/\/Subtype\s*\/Image/);
  });

  test("prints the title cut to fit in the header, and keeps it whole as the document's title", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    const createPdf = vi.spyOn(pdfMake, "createPdf");
    const title = "Убийство в оранжерее: Ștefan și țesătorul la Brașov. ".repeat(6).slice(0, 300);

    try {
      await makeStoryPdf({ ...request, title }, options());
      const definition = createPdf.mock.calls.at(-1)![0] as Node;
      const header = (definition.header as (page: number, pages: number, size: typeof A4) => Node)(1, 1, A4);
      const [row] = header.stack as Node[];
      const [, cell] = row.columns as Node[];

      expect((definition.info as { title: string }).title).toBe(title);
      expect(cell.text).toMatch(/…$/);
      expect(title.startsWith((cell.text as string).slice(0, -1))).toBe(true);
    } finally {
      createPdf.mockRestore();
    }
  });

  test("a logo that cannot be drawn is left out and the file is still made", async () => {
    const blob = await makeStoryPdf({ ...request, logoHref: "/missing" }, options());

    const pdf = (await bytes(blob)).toString("latin1");
    expect(pdf.startsWith("%PDF-")).toBe(true);
    expect(pdf).not.toMatch(/\/Subtype\s*\/Image/);
  });

  test("a face that cannot be read is a failure, not a file without text", async () => {
    // That the next attempt reads the faces again is fonts.test.ts's.
    const readFont: ReadFont = async (_source, file) => {
      if (file === "Onest-Medium.ttf") throw new Error("network down");
      return readFromDisk(FONTS, file);
    };

    await expect(makeStoryPdf(request, options({ fontSource: "offline", readFont }))).rejects.toThrow("network down");
  });
});

describe("measureColumns", () => {
  test("columns fill the text column in proportion to their widest content, as the screen lays a table out", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    const { tables } = storyBlocks("| a | b |\n|---|---|\n| 1 | a much longer cell than the first |");

    const [widths] = await measureColumns(pdfMake, tables);

    expect(widths).toHaveLength(2);
    expect(widths[0] + widths[1]).toBeCloseTo(MEASURE, 6);
    expect(widths[1]).toBeGreaterThan(4 * widths[0]);
  });

  test("a story without tables measures nothing", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);

    expect(await measureColumns(pdfMake, [])).toEqual([]);
  });
});

describe("fitTitle", () => {
  test("a title that fits the header is printed whole", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);

    expect(await fitTitle(pdfMake, "Убийство в оранжерее", titleSpace("mark"))).toBe("Убийство в оранжерее");
  });

  test("a 300-character title is cut, with an ellipsis, to the longest start that fits one line beside the logo", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    const title = "Убийство в оранжерее: Ștefan și țesătorul la Brașov. ".repeat(6).slice(0, 300);
    const space = titleSpace({ src: PIXEL, width: 480, height: 140 });

    const fitted = await fitTitle(pdfMake, title, space);
    const kept = fitted.slice(0, -1);

    expect(title).toHaveLength(300);
    expect(fitted.endsWith("…")).toBe(true);
    expect(title.startsWith(kept)).toBe(true);
    expect(await titleWidth(pdfMake, fitted)).toBeLessThanOrEqual(space - TITLE_SLACK);
    // One more letter and it would not.
    let more = kept.length + 1;
    while (title[more - 1] === " ") more += 1;
    expect(await titleWidth(pdfMake, `${title.slice(0, more)}…`)).toBeGreaterThan(space - TITLE_SLACK);
  });

  test("a title within half a point of the edge is cut, so rounding in the library's column cannot wrap it", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    const title = "Убийство в оранжерее";
    const width = await titleWidth(pdfMake, title);

    expect(TITLE_SLACK).toBe(0.5);
    expect(await fitTitle(pdfMake, title, width + TITLE_SLACK)).toBe(title);
    expect(await fitTitle(pdfMake, title, width + 0.25)).toMatch(/…$/);
  });

  test("collapses line breaks and runs of spaces, which would otherwise break the header's one line", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);

    expect(await fitTitle(pdfMake, "  Убийство\nв \t  оранжерее\r\n", titleSpace("mark"))).toBe("Убийство в оранжерее");
  });

  test("the cut never splits a letter from its combining accent", async () => {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    // "ș" written as s and a combining comma below, many times over.
    const title = "s\u0326".repeat(200);

    const fitted = await fitTitle(pdfMake, title, titleSpace("mark"));

    expect(fitted.endsWith("…")).toBe(true);
    expect(fitted.slice(0, -1).length % 2).toBe(0);
  });
});

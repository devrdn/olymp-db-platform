// @vitest-environment node
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { describe, expect, test, vi } from "vitest";

import type { ReadFont } from "@/lib/pdf/fonts";
import { preparedLibrary } from "@/lib/pdf/library";
import { embeddedFaces, missingGlyphs, namedFaces, readFromDisk } from "@/lib/pdf/pdf-test-support";
import type { PdfOptions } from "@/lib/pdf/render";
import { A4, MEASURE } from "@/lib/pdf/theme";
import type { Node } from "@/lib/pdf/types";

import { makeStoryPdf, measureColumns } from "./make-story-pdf";
import { storyBlocks } from "./story-markdown";

const FONTS = join(process.cwd(), "public/fonts/pdf");

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

const options = (overrides: Partial<PdfOptions> = {}): PdfOptions => ({
  fontSource: FONTS,
  readFont: readFromDisk,
  drawLogo: async () => Promise.reject(new Error("no canvas in a test")),
  ...overrides,
});

async function bytes(blob: Blob): Promise<Buffer> {
  return Buffer.from(await blob.arrayBuffer());
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
  test("draws the installation's logo from the address it is given", async () => {
    const drawLogo = vi.fn(async () => Promise.reject(new Error("broken")));

    await makeStoryPdf({ ...request, logoHref: "/api/v1/settings/images/logo?v=abc" }, options({ drawLogo }));

    expect(drawLogo).toHaveBeenCalledWith("/api/v1/settings/images/logo?v=abc");
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
});

describe("the story PDF's page breaks", () => {
  /** The pages a laid-out node is drawn on; the library leaves its positions on the node. */
  const pagesOf = (node: Node) => [...new Set((node.positions as { pageNumber: number }[]).map((position) => position.pageNumber))];

  /** The story laid out: each of its blocks, whether it is a heading, and its pages. */
  async function layOut(markdown: string) {
    const pdfMake = await preparedLibrary(FONTS, readFromDisk);
    const createPdf = vi.spyOn(pdfMake, "createPdf");
    try {
      const pdf = await bytes(await makeStoryPdf({ ...request, markdown }, options()));
      const definition = createPdf.mock.calls.at(-1)![0] as Node;
      const story = ((definition.content as Node[])[1].stack as Node[]).map((node) => ({ heading: Boolean(node.headlineLevel), pages: pagesOf(node) }));
      return { pdf, story };
    } finally {
      createPdf.mockRestore();
    }
  }

  // Sections of growing length, so that across the pages some heading falls at a page's foot.
  const paragraph = (k: number) => "Au găsit-o încuiată. ".repeat(4 + k * 3);

  test("a heading that would end a page starts the next one, with its first paragraph", async () => {
    const { pdf, story } = await layOut(Array.from({ length: 30 }, (_, k) => `## Раздел ${k + 1}\n\n${paragraph(k)}`).join("\n\n"));
    const headings = story.flatMap((node, index) => (node.heading ? [{ node, before: story[index - 1], after: story[index + 1] }] : []));

    expect(headings).toHaveLength(30);
    for (const { node, after } of headings) expect(after.pages[0]).toBe(node.pages[0]);
    // The fixture does put a heading at a page's foot: one now opens its page.
    expect(headings.some(({ node, before }) => before && before.pages.at(-1)! < node.pages[0])).toBe(true);
    expect(missingGlyphs(pdf)).toBe(0);
  });

  test("headings that follow one another move to the next page together, with the first paragraph after them", async () => {
    const { story } = await layOut(Array.from({ length: 30 }, (_, k) => `## Раздел ${k + 1}\n\n### Ștefan ${k + 1}\n\n${paragraph(k)}`).join("\n\n"));
    const runs = story.flatMap((node, index) => (node.heading && !story[index - 1]?.heading ? [{ node, before: story[index - 1], after: story[index + 2] }] : []));

    expect(runs).toHaveLength(30);
    for (const { node, after } of runs) expect(after.pages[0]).toBe(node.pages[0]);
    expect(runs.some(({ node, before }) => before && before.pages.at(-1)! < node.pages[0])).toBe(true);
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

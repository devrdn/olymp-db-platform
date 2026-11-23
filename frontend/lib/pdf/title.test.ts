// @vitest-environment node
import { join } from "node:path";
import { describe, expect, test } from "vitest";

import { preparedLibrary } from "./library";
import { PIXEL, readFromDisk } from "./pdf-test-support";
import { titleSpace } from "./running-header";
import { TITLE_SLACK, fitTitle, titleWidth } from "./title";

const FONTS = join(process.cwd(), "public/fonts/pdf");

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

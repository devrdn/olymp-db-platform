// @vitest-environment node
import { join } from "node:path";
import { describe, expect, test, vi } from "vitest";

import type { ReadFont } from "./fonts";
import { PIXEL, readFromDisk } from "./pdf-test-support";
import { renderPdf, type PdfBuild, type PdfOptions } from "./render";
import { titleSpace } from "./running-header";
import { fitTitle } from "./title";
import type { Node } from "./types";

const FONTS = join(process.cwd(), "public/fonts/pdf");

const options = (overrides: Partial<PdfOptions> = {}): PdfOptions => ({
  fontSource: FONTS,
  readFont: readFromDisk,
  drawLogo: async () => Promise.reject(new Error("no canvas in a test")),
  ...overrides,
});

/** A one-line document: the fitted title, and the uploaded logo when there is one. */
const document = ({ logo, headerTitle }: PdfBuild): Node => ({
  pageSize: "A4",
  defaultStyle: { font: "Onest" },
  images: logo && logo !== "mark" ? { logo: logo.src } : {},
  content: [{ text: headerTitle }, ...(logo && logo !== "mark" ? [{ image: "logo", width: 20 }] : [])],
});

const text = async (blob: Blob) => Buffer.from(await blob.arrayBuffer()).toString("latin1");

describe("renderPdf", () => {
  test("makes a PDF file with the uploaded logo, drawn", async () => {
    const drawLogo = vi.fn(async () => ({ src: PIXEL, width: 200, height: 60 }));

    const blob = await renderPdf({ title: "Crima", logoHref: "/api/v1/settings/images/logo?v=abc" }, document, options({ drawLogo }));

    expect(drawLogo).toHaveBeenCalledWith("/api/v1/settings/images/logo?v=abc");
    expect(blob.type).toBe("application/pdf");
    expect(await text(blob)).toMatch(/\/Subtype\s*\/Image/);
  });

  test("with no uploaded logo it hands the document the product mark and loads nothing", async () => {
    const drawLogo = vi.fn();
    const seen = vi.fn(document);

    const pdf = await text(await renderPdf({ title: "Crima" }, seen, options({ drawLogo })));

    expect(drawLogo).not.toHaveBeenCalled();
    expect(seen.mock.calls[0][0].logo).toBe("mark");
    expect(pdf).not.toMatch(/\/Subtype\s*\/Image/);
  });

  test("a logo that cannot be drawn is left out and the file is still made", async () => {
    const seen = vi.fn(document);

    const pdf = await text(await renderPdf({ title: "Crima", logoHref: "/missing" }, seen, options()));

    expect(seen.mock.calls[0][0].logo).toBeNull();
    expect(pdf.startsWith("%PDF-")).toBe(true);
    expect(pdf).not.toMatch(/\/Subtype\s*\/Image/);
  });

  test("fits the title to the room the logo and the width leave it", async () => {
    const seen = vi.fn(document);
    const title = "Убийство в оранжерее: Ștefan și țesătorul la Brașov. ".repeat(6);

    await renderPdf({ title, width: 300 }, seen, options());

    const { pdfMake, headerTitle } = seen.mock.calls[0][0];
    expect(headerTitle).toBe(await fitTitle(pdfMake, title, titleSpace("mark", 300)));
    expect(headerTitle).toMatch(/…$/);
  });

  test("a face that cannot be read is a failure, not a file without text", async () => {
    // That the next attempt reads the faces again is fonts.test.ts's.
    const readFont: ReadFont = async (_source, file) => {
      if (file === "Onest-Medium.ttf") throw new Error("network down");
      return readFromDisk(FONTS, file);
    };

    await expect(renderPdf({ title: "Crima" }, document, options({ fontSource: "offline", readFont }))).rejects.toThrow("network down");
  });
});

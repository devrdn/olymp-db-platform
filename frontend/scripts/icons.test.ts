// @vitest-environment node
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { inflateSync } from "node:zlib";

import { describe, expect, test } from "vitest";

import { FACE, PAINT, markSvg } from "../lib/brand/marks.mjs";

import { appleIcon, faviconIco, iconSvg, palette, rasterise } from "./icons.mjs";

const ROOT = join(__dirname, "..");
const read = (path: string) => readFileSync(join(ROOT, path));

/** A PNG's size and its unfiltered RGBA rows; the encoder writes filter 0 only. */
function decodePng(png: Buffer) {
  expect(png.subarray(0, 8)).toEqual(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
  let width = 0;
  let height = 0;
  const data: Buffer[] = [];
  for (let at = 8; at < png.length; ) {
    const length = png.readUInt32BE(at);
    const type = png.toString("latin1", at + 4, at + 8);
    const body = png.subarray(at + 8, at + 8 + length);
    if (type === "IHDR") {
      width = body.readUInt32BE(0);
      height = body.readUInt32BE(4);
    }
    if (type === "IDAT") data.push(body);
    at += 12 + length;
  }
  return { width, height, rows: inflateSync(Buffer.concat(data)) };
}

/** The images inside an ICO, each with the size its directory entry claims. */
function icoEntries(ico: Buffer) {
  expect(ico.readUInt16LE(0)).toBe(0);
  expect(ico.readUInt16LE(2)).toBe(1);
  return Array.from({ length: ico.readUInt16LE(4) }, (_, i) => {
    const entry = 6 + i * 16;
    const size = ico.readUInt32LE(entry + 8);
    const offset = ico.readUInt32LE(entry + 12);
    return { claimed: ico[entry] || 256, png: ico.subarray(offset, offset + size) };
  });
}

/** A pixel of an RGBA buffer `size` pixels wide. */
const pixelAt = (pixels: Buffer, size: number, x: number, y: number) =>
  Array.from(pixels.subarray((y * size + x) * 4, (y * size + x) * 4 + 4));
const rgb = (colour: string) => [1, 3, 5].map((i) => parseInt(colour.slice(i, i + 2), 16));

/**
 * The committed icons are what the generator writes, so a change to the face
 * or to the logo's colours fails here until `npm run icons` is run. Rasters are
 * compared by their pixels: zlib's bytes may differ between Node versions.
 */
describe("the default icons", () => {
  test("draw the cat's face from lib/brand/marks.mjs", () => {
    const { ink, chalk, darkInk, darkChalk } = palette();

    expect(iconSvg()).toBe(`${markSvg(FACE, { ink, chalk }, { ink: darkInk, chalk: darkChalk })}\n`);
  });

  test("take the logo's colours of each theme from the tokens", () => {
    const tokens = readFileSync(join(ROOT, "styles/tokens.css"), "utf8");
    const { ink, chalk, darkInk, darkChalk } = palette();

    expect(tokens).toMatch(new RegExp(`--logo-ink: ${ink};`));
    expect(tokens).toMatch(new RegExp(`--logo-chalk: ${chalk};`));
    expect(tokens).toMatch(new RegExp(`--dark-logo-ink: ${darkInk};`));
    expect(tokens).toMatch(new RegExp(`--dark-logo-chalk: ${darkChalk};`));
  });

  test("swap the black and cream patches with the browser's colour scheme in the SVG", () => {
    const { darkInk, darkChalk } = palette();

    expect(iconSvg()).toContain(`@media (prefers-color-scheme: dark){.ink{fill:${darkInk}}.chalk{fill:${darkChalk}}`);
  });

  test("are committed as the generator writes them", () => {
    expect(read("public/icon.svg").toString()).toBe(iconSvg());

    const apple = decodePng(read("public/apple-icon.png"));
    expect([apple.width, apple.height]).toEqual([180, 180]);
    expect(apple.rows).toEqual(decodePng(appleIcon()).rows);

    const committed = icoEntries(read("public/favicon.ico"));
    const generated = icoEntries(faviconIco());
    expect(committed.map((entry) => entry.claimed)).toEqual([32]);
    expect(committed.map((entry) => decodePng(entry.png))).toEqual(generated.map((entry) => decodePng(entry.png)));
  });

  test("rasterise the face in its colours on a clear ground", () => {
    // At 32 pixels a unit of the face is one pixel. Pixel (20, 11) lies in the
    // terracotta of the face, (5, 5) in the black ear, (0, 31) outside it.
    const pixels = rasterise(32, FACE, { scale: 1, x: 0, y: 0, ground: null });

    expect(pixelAt(pixels, 32, 20, 11)).toEqual([...rgb(PAINT.terracotta), 255]);
    expect(pixelAt(pixels, 32, 5, 5)).toEqual([...rgb(palette().ink), 255]);
    expect(pixelAt(pixels, 32, 0, 31)[3]).toBe(0);
  });

  test("put the detailed face on an opaque cream square for the home screen", () => {
    const { rows } = decodePng(appleIcon());
    const row = 1 + 180 * 4;
    const alphas = Array.from({ length: 180 * 180 }, (_, i) => rows[Math.floor(i / 180) * row + 1 + (i % 180) * 4 + 3]);

    expect(new Set(alphas)).toEqual(new Set([255]));
    expect(Array.from(rows.subarray(1, 5))).toEqual([...rgb(palette().chalk), 255]);
  });
});

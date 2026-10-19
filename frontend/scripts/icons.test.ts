// @vitest-environment node
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { inflateSync } from "node:zlib";

import { describe, expect, test } from "vitest";

import { appleIcon, faviconIco, iconSvg, markPath, palette, rasterise } from "./icons.mjs";

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

/**
 * The committed icons are what the generator writes, so a change to the mark
 * or to the ink colours fails here until `npm run icons` is run. Rasters are
 * compared by their pixels: zlib's bytes may differ between Node versions.
 */
describe("the default icons", () => {
  test("draw the path components/layout/mark.tsx draws", () => {
    const mark = readFileSync(join(ROOT, "components/layout/mark.tsx"), "utf8");

    expect(mark).toContain(`d="${markPath()}"`);
    expect(iconSvg()).toContain(`d="${markPath()}"`);
  });

  test("take the ink of each theme from the tokens", () => {
    const tokens = readFileSync(join(ROOT, "styles/tokens.css"), "utf8");

    expect(tokens).toMatch(new RegExp(`--ink: ${palette().ink};`));
    expect(tokens).toMatch(new RegExp(`--dark-ink: ${palette().darkInk};`));
    expect(tokens).toMatch(new RegExp(`--bg: ${palette().bg};`));
  });

  test("follow the browser's colour scheme in the SVG", () => {
    const { ink, darkInk } = palette();

    expect(iconSvg()).toMatch(new RegExp(`path\\{fill:${ink}\\}@media \\(prefers-color-scheme: ?dark\\)\\{path\\{fill:${darkInk}\\}\\}`));
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

  test("rasterise the card in ink with its rules cut out to the ground", () => {
    // At 32 pixels a unit is two: pixel (5, 9) lies inside the card's body and
    // pixel (10, 15) inside the first rule, which spans y 14.4 to 16.8.
    const pixel = rasterise(32, { scale: 2, offset: 0, radius: 0 });
    const at = (x: number, y: number) => Array.from(pixel.subarray((y * 32 + x) * 4, (y * 32 + x) * 4 + 4));
    const hex = (colour: string) => [1, 3, 5].map((i) => parseInt(colour.slice(i, i + 2), 16));

    expect(at(5, 9)).toEqual([...hex(palette().ink), 255]);
    expect(at(10, 15)).toEqual([...hex(palette().bg), 255]);
  });
});

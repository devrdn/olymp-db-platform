#!/usr/bin/env node
/**
 * The default tab and home-screen icons, drawn from the product mark.
 *
 * The path is read from components/layout/mark.tsx and the colours from
 * styles/tokens.css, so the icons follow both; scripts/icons.test.ts fails
 * while the committed files differ from what this writes. No dependency: the
 * mark is straight lines only, so a supersampled even-odd fill and Node's zlib
 * are enough to draw and encode it.
 *
 *   npm run icons    writes public/icon.svg, public/favicon.ico, public/apple-icon.png
 */

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

/** The mark's path data, as mark.tsx draws it in its 16-unit box. */
export function markPath() {
  const source = readFileSync(join(ROOT, "components", "layout", "mark.tsx"), "utf8");
  const match = source.match(/<path d="([^"]+)"/);
  if (!match) throw new Error("components/layout/mark.tsx has no <path d=…>");
  return match[1];
}

/** The light theme's ink and ground and the dark theme's ink, as hex. */
export function palette() {
  const tokens = readFileSync(join(ROOT, "styles", "tokens.css"), "utf8");
  const token = (name) => {
    const match = tokens.match(new RegExp(`--${name}: (#[0-9a-f]{6});`));
    if (!match) throw new Error(`styles/tokens.css has no hex --${name}`);
    return match[1];
  };
  return { ink: token("ink"), bg: token("bg"), darkInk: token("dark-ink") };
}

/**
 * The tab icon for browsers that read SVG favicons. The scheme query follows
 * the browser's own light or dark setting, which is what its chrome follows.
 */
export function iconSvg() {
  const { ink, darkInk } = palette();
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">` +
    `<style>path{fill:${ink}}@media (prefers-color-scheme: dark){path{fill:${darkInk}}}</style>` +
    `<path fill-rule="evenodd" d="${markPath()}"/></svg>\n`
  );
}

/**
 * The path's closed outlines as point lists. Only the commands the mark uses
 * are understood (M, L, H, V, Z, absolute and relative); a curve is refused
 * rather than drawn wrong.
 */
function outlines(d) {
  const tokens = d.match(/[a-zA-Z]|-?\d*\.?\d+/g);
  const shapes = [];
  let shape = [];
  let x = 0;
  let y = 0;
  let command = "";
  for (let i = 0; i < tokens.length; ) {
    if (/[a-zA-Z]/.test(tokens[i])) command = tokens[i++];
    const number = () => Number(tokens[i++]);
    const relative = command === command.toLowerCase();
    switch (command.toUpperCase()) {
      case "M":
        if (shape.length) shapes.push(shape);
        x = number() + (relative ? x : 0);
        y = number() + (relative ? y : 0);
        shape = [[x, y]];
        command = relative ? "l" : "L";
        break;
      case "L":
        x = number() + (relative ? x : 0);
        y = number() + (relative ? y : 0);
        shape.push([x, y]);
        break;
      case "H":
        x = number() + (relative ? x : 0);
        shape.push([x, y]);
        break;
      case "V":
        y = number() + (relative ? y : 0);
        shape.push([x, y]);
        break;
      case "Z":
        shapes.push(shape);
        [x, y] = shape[0];
        shape = [];
        break;
      default:
        throw new Error(`the mark's path uses "${command}", which this script cannot draw`);
    }
  }
  if (shape.length) shapes.push(shape);
  return shapes;
}

/**
 * Where a horizontal line at height `y` crosses the outlines, sorted. Even-odd:
 * a point is inside when an odd number of crossings lie to its right.
 */
function crossings(shapes, y) {
  const found = [];
  for (const shape of shapes) {
    for (let i = 0, j = shape.length - 1; i < shape.length; j = i++) {
      const [xi, yi] = shape[i];
      const [xj, yj] = shape[j];
      if (yi > y !== yj > y) found.push(((xj - xi) * (y - yi)) / (yj - yi) + xi);
    }
  }
  return found.sort((a, b) => a - b);
}

function inRoundedSquare(size, radius, x, y) {
  const cx = Math.min(Math.max(x, radius), size - radius);
  const cy = Math.min(Math.max(y, radius), size - radius);
  return (x - cx) ** 2 + (y - cy) ** 2 <= radius ** 2;
}

/** Samples per pixel edge; 16 gives 256 coverage levels, as many as a byte holds. */
const SAMPLES = 16;

/**
 * RGBA pixels of the mark in ink on a ground-coloured tile. A raster cannot
 * follow the colour scheme, so the tile carries its own ground and the mark
 * reads on light and dark chrome alike. `scale` is pixels per mark unit,
 * `offset` where the mark's box starts, `radius` the tile's corner in pixels.
 */
export function rasterise(size, { scale, offset, radius }) {
  const shapes = outlines(markPath());
  const { ink, bg } = palette();
  const rgb = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
  const [inkRgb, bgRgb] = [rgb(ink), rgb(bg)];
  const pixels = Buffer.alloc(size * size * 4);
  const tile = new Uint16Array(size);
  const mark = new Uint16Array(size);
  for (let y = 0; y < size; y++) {
    tile.fill(0);
    mark.fill(0);
    for (let sy = 0; sy < SAMPLES; sy++) {
      const py = y + (sy + 0.5) / SAMPLES;
      const edges = crossings(shapes, (py - offset) / scale).map((x) => x * scale + offset);
      let passed = 0;
      for (let x = 0; x < size; x++) {
        for (let sx = 0; sx < SAMPLES; sx++) {
          const px = x + (sx + 0.5) / SAMPLES;
          while (passed < edges.length && edges[passed] <= px) passed++;
          if (!inRoundedSquare(size, radius, px, py)) continue;
          tile[x]++;
          if ((edges.length - passed) % 2 === 1) mark[x]++;
        }
      }
    }
    for (let x = 0; x < size; x++) {
      const at = (y * size + x) * 4;
      const share = tile[x] ? mark[x] / tile[x] : 0;
      for (let c = 0; c < 3; c++) {
        pixels[at + c] = Math.round(bgRgb[c] + (inkRgb[c] - bgRgb[c]) * share);
      }
      pixels[at + 3] = Math.round((255 * tile[x]) / SAMPLES ** 2);
    }
  }
  return pixels;
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(bytes) {
  let c = 0xffffffff;
  for (const byte of bytes) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, body) {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(body.length, 0);
  head.write(type, 4, "latin1");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), body])), 0);
  return Buffer.concat([head, body, crc]);
}

/** An RGBA PNG; every row uses filter 0, which keeps the encoder a few lines. */
function png(size, pixels) {
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header.set([8, 6, 0, 0, 0], 8);
  const rows = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y++) {
    pixels.copy(rows, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(rows, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

/**
 * The tab icon for browsers without SVG favicons: one 32-pixel image, PNG
 * inside an ICO. At this size a mark unit is two pixels, and the mark's own
 * margin in its box is the tile's padding.
 */
export function faviconIco() {
  const size = 32;
  const image = png(size, rasterise(size, { scale: 2, offset: 0, radius: 6 }));
  const header = Buffer.alloc(6 + 16);
  header.writeUInt16LE(0, 0);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(1, 4);
  header.set([size, size, 0, 0], 6);
  header.writeUInt16LE(1, 10);
  header.writeUInt16LE(32, 12);
  header.writeUInt32LE(image.length, 14);
  header.writeUInt32LE(header.length, 18);
  return Buffer.concat([header, image]);
}

/**
 * The home-screen icon: 180 pixels, square and opaque, since iOS rounds the
 * corners itself and fills transparency with black. The mark's box takes the
 * middle 120 pixels.
 */
export function appleIcon() {
  const size = 180;
  return png(size, rasterise(size, { scale: 7.5, offset: 30, radius: 0 }));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const out = (name, body) => {
    writeFileSync(join(ROOT, "public", name), body);
    console.log(`wrote public/${name}`);
  };
  out("icon.svg", iconSvg());
  out("favicon.ico", faviconIco());
  out("apple-icon.png", appleIcon());
}

#!/usr/bin/env node
/**
 * The default tab and home-screen icons, drawn from the cat's face in
 * lib/brand/marks.mjs.
 *
 * The logo's black and cream come from styles/tokens.css, so the icons follow
 * both; scripts/icons.test.ts fails while the committed files differ from what
 * this writes. No dependency: the faces are straight lines and quadratic
 * curves only, so a supersampled even-odd fill and Node's zlib are enough to
 * draw and encode them.
 *
 *   npm run icons    writes public/icon.svg, public/favicon.ico, public/apple-icon.png
 */

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

import { FACE, FACE_DETAILED, PAINT, markSvg } from "../lib/brand/marks.mjs";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

/** The logo's black and cream in each theme, as hex. */
export function palette() {
  const tokens = readFileSync(join(ROOT, "styles", "tokens.css"), "utf8");
  const token = (name) => {
    const match = tokens.match(new RegExp(`--${name}: (#[0-9a-f]{6});`));
    if (!match) throw new Error(`styles/tokens.css has no hex --${name}`);
    return match[1];
  };
  return {
    ink: token("logo-ink"),
    chalk: token("logo-chalk"),
    darkInk: token("dark-logo-ink"),
    darkChalk: token("dark-logo-chalk"),
  };
}

/**
 * The tab icon for browsers that read SVG favicons. Its black and cream swap
 * with the browser's own light or dark setting, which is what its tabs follow.
 */
export function iconSvg() {
  const { ink, chalk, darkInk, darkChalk } = palette();
  return `${markSvg(FACE, { ink, chalk }, { ink: darkInk, chalk: darkChalk })}\n`;
}

/** A quadratic curve becomes this many straight segments; at 180 px that is finer than a pixel. */
const CURVE_STEPS = 16;

/**
 * A path's closed outlines as point lists. Only what the faces use is
 * understood (absolute M, L, Q, Z); anything else is refused rather than
 * drawn wrong.
 */
function pathOutlines(d) {
  const tokens = d.match(/[a-zA-Z]|-?\d*\.?\d+/g);
  const shapes = [];
  let shape = [];
  let command = "";
  for (let i = 0; i < tokens.length; ) {
    if (/[a-zA-Z]/.test(tokens[i])) command = tokens[i++];
    const number = () => Number(tokens[i++]);
    switch (command) {
      case "M":
        if (shape.length) shapes.push(shape);
        shape = [[number(), number()]];
        command = "L";
        break;
      case "L":
        shape.push([number(), number()]);
        break;
      case "Q": {
        const [x0, y0] = shape[shape.length - 1];
        const [cx, cy, x1, y1] = [number(), number(), number(), number()];
        for (let step = 1; step <= CURVE_STEPS; step++) {
          const t = step / CURVE_STEPS;
          const [a, b, c] = [(1 - t) ** 2, 2 * (1 - t) * t, t ** 2];
          shape.push([a * x0 + b * cx + c * x1, a * y0 + b * cy + c * y1]);
        }
        break;
      }
      case "Z":
        shapes.push(shape);
        shape = [];
        break;
      default:
        throw new Error(`a face's path uses "${command}", which this script cannot draw`);
    }
  }
  if (shape.length) shapes.push(shape);
  return shapes;
}

/** A shape's closed outlines, in the mark's own units. */
function outlines({ el, attrs }) {
  if (el === "polygon") {
    const numbers = attrs.points.trim().split(/[\s,]+/).map(Number);
    return [Array.from({ length: numbers.length / 2 }, (_, i) => [numbers[2 * i], numbers[2 * i + 1]])];
  }
  if (el === "rect") {
    const [x, y, w, h] = ["x", "y", "width", "height"].map((key) => Number(attrs[key]));
    return [[[x, y], [x + w, y], [x + w, y + h], [x, y + h]]];
  }
  if (el === "path") return pathOutlines(attrs.d);
  throw new Error(`a face uses <${el}>, which this script cannot draw`);
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

/** Samples per pixel edge; 16 gives 256 coverage levels, as many as a byte holds. */
const SAMPLES = 16;

/** How much of each pixel the outlines cover, from 0 to 1. */
function coverage(shapes, size) {
  const covered = new Float64Array(size * size);
  const ys = shapes.flat().map(([, y]) => y);
  const [top, bottom] = [Math.max(0, Math.floor(Math.min(...ys))), Math.min(size, Math.ceil(Math.max(...ys)))];
  for (let y = top; y < bottom; y++) {
    for (let sy = 0; sy < SAMPLES; sy++) {
      const edges = crossings(shapes, y + (sy + 0.5) / SAMPLES);
      for (let e = 0; e + 1 < edges.length; e += 2) {
        // Samples whose centres lie between a pair of crossings are inside.
        const first = Math.max(0, Math.ceil(edges[e] * SAMPLES - 0.5));
        const last = Math.min(size * SAMPLES - 1, Math.ceil(edges[e + 1] * SAMPLES - 0.5) - 1);
        for (let s = first; s <= last; s++) covered[y * size + Math.floor(s / SAMPLES)] += 1 / SAMPLES ** 2;
      }
    }
  }
  return covered;
}

const rgb = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));

/**
 * RGBA pixels of a face, its shapes laid over one another in order, in the
 * light theme's colours: a raster cannot follow the colour scheme. `scale` is
 * pixels per mark unit, (`x`, `y`) where the mark's box starts, and `ground`
 * the colour under it, or null for a clear one.
 */
export function rasterise(size, mark, { scale, x, y, ground }) {
  const [left, top] = mark.viewBox.split(" ").map(Number);
  const { ink, chalk } = palette();
  const colour = { ink, chalk, ...PAINT };
  // Premultiplied colour and alpha, composited in floating point.
  const image = new Float64Array(size * size * 4);
  if (ground) {
    const [r, g, b] = rgb(ground);
    for (let at = 0; at < image.length; at += 4) image.set([r, g, b, 1], at);
  }
  for (const shape of mark.shapes) {
    if (!(shape.paint in colour)) throw new Error(`a face paints with "${shape.paint}", which this script cannot draw`);
    const [r, g, b] = rgb(colour[shape.paint]);
    const placed = outlines(shape).map((points) => points.map(([px, py]) => [x + (px - left) * scale, y + (py - top) * scale]));
    const covered = coverage(placed, size);
    for (let i = 0; i < covered.length; i++) {
      const a = covered[i];
      if (!a) continue;
      const at = i * 4;
      image[at] = r * a + image[at] * (1 - a);
      image[at + 1] = g * a + image[at + 1] * (1 - a);
      image[at + 2] = b * a + image[at + 2] * (1 - a);
      image[at + 3] = a + image[at + 3] * (1 - a);
    }
  }
  const pixels = Buffer.alloc(size * size * 4);
  for (let at = 0; at < image.length; at += 4) {
    const alpha = image[at + 3];
    for (let c = 0; c < 3; c++) pixels[at + c] = alpha ? Math.round(image[at + c] / alpha) : 0;
    pixels[at + 3] = Math.round(alpha * 255);
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
 * The tab icon for browsers without SVG favicons: the face at 32 pixels, one
 * pixel to a unit of its 32-unit grid, PNG inside an ICO, on a clear ground.
 */
export function faviconIco() {
  const size = 32;
  const image = png(size, rasterise(size, FACE, { scale: 1, x: 0, y: 0, ground: null }));
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
 * corners itself and fills transparency with black. The detailed face sits on
 * the cream as the design canvas places it: 104 of a 150-pixel tile's width,
 * centred.
 */
export function appleIcon() {
  const size = 180;
  const [, , width, height] = FACE_DETAILED.viewBox.split(" ").map(Number);
  const scale = (size * 104) / (150 * width);
  const place = { scale, x: (size - width * scale) / 2, y: (size - height * scale) / 2, ground: palette().chalk };
  return png(size, rasterise(size, FACE_DETAILED, place));
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

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, test } from "vitest";

import { A4, ASCENDER, COLOR, MEASURE, MM, NATURAL, PAGE, PT_PER_REM, STEP, type Face } from "./theme";

const globals = readFileSync(join(process.cwd(), "app/globals.css"), "utf8");
const tokens = readFileSync(join(process.cwd(), "styles/tokens.css"), "utf8");

/** A step of the type scale in `globals.css`, or one of its properties, as written there. */
function scale(step: string, property?: string): string {
  const name = `--text-${step}${property ? `--${property}` : ""}`;
  const match = new RegExp(`${name}:\\s*([^;]+);`).exec(globals);
  if (!match) throw new Error(`${name} is not in app/globals.css`);
  return match[1].trim();
}

const rem = (value: string) => {
  if (!value.endsWith("rem")) throw new Error(`${value} is not in rem`);
  return Number.parseFloat(value);
};
const em = (value: string) => (value.endsWith("em") ? Number.parseFloat(value) : Number.NaN);

/** The light theme's value of a colour token: the first `:root` block that sets it. */
function lightToken(name: string): string {
  const match = new RegExp(`--${name}:\\s*([^;]+);`).exec(tokens);
  if (!match) throw new Error(`--${name} is not in styles/tokens.css`);
  return match[1].trim();
}

/** A colour as it prints on white paper: an `rgb(r g b / a)` token is laid over white. */
function onPaper(value: string): string {
  if (value.startsWith("#")) return value.toLowerCase();
  const match = /^rgb\((\d+) (\d+) (\d+) \/ ([\d.]+)\)$/.exec(value);
  if (!match) throw new Error(`cannot read ${value}`);
  const alpha = Number(match[4]);
  return `#${[1, 2, 3]
    .map((i) => Math.round(255 + alpha * (Number(match[i]) - 255)))
    .map((channel) => channel.toString(16).padStart(2, "0"))
    .join("")}`;
}

/** Ascender and descender from a TrueType file's `hhea` table, in em. */
function verticalMetrics(file: string) {
  const font = readFileSync(join(process.cwd(), "public/fonts/pdf", file));
  const tables = new Map<string, number>();
  for (let i = 0; i < font.readUInt16BE(4); i++) {
    const record = 12 + i * 16;
    tables.set(font.toString("latin1", record, record + 4), font.readUInt32BE(record + 8));
  }
  const unitsPerEm = font.readUInt16BE(tables.get("head")! + 18);
  const hhea = tables.get("hhea")!;
  return { ascender: font.readInt16BE(hhea + 4) / unitsPerEm, descender: font.readInt16BE(hhea + 6) / unitsPerEm };
}

describe("the PDF's type scale is the screen's, converted at one ratio", () => {
  test.each([
    ["narrative", "narrative"],
    ["h3", "h3"],
    ["row", "row"],
    ["body", "body"],
    ["label", "label"],
    ["small", "small"],
  ] as const)("%s is text-%s", (step, token) => {
    expect(STEP[step].size).toBeCloseTo(rem(scale(token)) * PT_PER_REM, 6);
    expect(STEP[step].leading).toBe(Number(scale(token, "line-height")));
    const tracking = globals.includes(`--text-${token}--letter-spacing:`) ? em(scale(token, "letter-spacing")) : 0;
    expect(STEP[step].tracking).toBe(tracking);
  });

  test("the heading takes text-h2's clamp with the sheet as the viewport", () => {
    expect(scale("h2")).toBe("clamp(2rem, 3.6vw, 3.25rem)");
    // 595.28 pt at 10 pt per 16 px is 952.4 px, and 3.6% of it is 34.29 px.
    expect(STEP.h2.size).toBeCloseTo((0.036 * 595.28 * 1.6 * PT_PER_REM) / 16, 6);
    expect(STEP.h2.leading).toBe(Number(scale("h2", "line-height")));
    expect(STEP.h2.tracking).toBe(em(scale("h2", "letter-spacing")));
  });

  test("weights follow the tokens: 500 where the step says so, and the narrative at 400", () => {
    expect(STEP.h3.medium).toBe(scale("h3", "font-weight") === "500");
    expect(STEP.row.medium).toBe(scale("row", "font-weight") === "500");
    expect(scale("narrative", "font-weight")).toBe("400");
    expect(STEP.h2.medium).toBe(false);
  });
});

describe("the PDF's page is laid out in millimetres, not in the screen's measure", () => {
  test("a millimetre is 72/25.4 pt", () => {
    expect(MM).toBeCloseTo(2.834645669, 8);
  });

  test("the sides are 20 mm each, and the column is the sheet's width between them: 170 mm", () => {
    expect(PAGE.side).toBeCloseTo(20 * MM, 6);
    expect(MEASURE).toBeCloseTo(170 * MM, 1);
    expect(A4.width - 2 * PAGE.side).toBeCloseTo(MEASURE, 6);
  });

  test("the bottom margin is 20 mm and the page number sits 20 pt below the text", () => {
    expect(PAGE.bottom).toBeCloseTo(20 * MM, 6);
    expect(PAGE.footer).toBe(20);
  });
});

describe("the PDF's colours are the light theme's, as they print on white", () => {
  test.each([
    ["ink", "ink"],
    ["ink2", "ink-2"],
    ["ink3", "ink-3"],
    ["sunk", "sunk"],
    ["accent", "accent-ink"],
    ["paper", "bg"],
    ["line", "line"],
    ["line2", "line-2"],
    ["ann", "ann"],
  ] as const)("%s is --%s", (key, token) => {
    expect(COLOR[key]).toBe(onPaper(lightToken(token)));
  });
});

describe("the faces' vertical metrics are the files'", () => {
  test.each([
    ["Literata", "Literata-Regular.ttf"],
    ["Onest", "Onest-Regular.ttf"],
    ["JetBrainsMono", "JetBrainsMono-Regular.ttf"],
  ] as const)("%s", (face: Face, file) => {
    const { ascender, descender } = verticalMetrics(file);
    expect(ASCENDER[face]).toBeCloseTo(ascender, 3);
    expect(NATURAL[face]).toBeCloseTo(ascender - descender, 3);
  });
});

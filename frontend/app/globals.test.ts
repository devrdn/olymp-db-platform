// @vitest-environment node
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, test } from "vitest";

const css = readFileSync(join(__dirname, "globals.css"), "utf8");

/** The body of the block whose header starts at `start`. */
function blockFrom(start: number): string {
  let depth = 0;
  for (let i = css.indexOf("{", start); i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(css.indexOf("{", start) + 1, i);
  }
  throw new Error(`the block at ${start} is not closed`);
}

/** The body of the first block opened by `header`. */
function block(header: string): string {
  const start = css.indexOf(header);
  expect(start, `${header} is in globals.css`).toBeGreaterThanOrEqual(0);
  return blockFrom(start);
}

/** A phone held either way: below `narrow`, or a touch screen below `wide`. */
const PHONE = "@media (width < 47.5rem), (pointer: coarse) and (width < 64rem)";

/** Each step of the type scale with its size, or null where it is not a fixed rem. */
function steps(): Map<string, number | null> {
  const found = new Map<string, number | null>();
  for (const [, name, value] of block("@theme {").matchAll(/^\s*--text-([a-z0-9-]+):\s*([^;]+);/gm)) {
    if (name.includes("--") || value.trim() === "initial") continue;
    const rem = /^([\d.]+)rem$/.exec(value.trim());
    found.set(name, rem ? Number(rem[1]) : null);
  }
  return found;
}

// jsdom cannot evaluate a media query or a cascade, so this asserts the rule a
// phone relies on: iOS Safari zooms the page when a focused field is set under
// 16px. The computed sizes were checked in a real browser.
describe("text entry on a phone", () => {
  const rules = block(PHONE);

  test("draws every step under 16px at 1rem on a field, and leaves the larger steps alone", () => {
    const scale = steps();
    expect([...scale.keys()]).toEqual(expect.arrayContaining(["control", "body", "narrative"]));
    for (const [name, size] of scale) {
      if (size !== null && size < 1) expect(rules, name).toMatch(new RegExp(`--text-${name}:\\s*1rem;`));
      else expect(rules, name).not.toContain(`--text-${name}:`);
    }
  });

  test("raises a field that names no step to 16px without lowering a larger inherited size", () => {
    expect(rules).toMatch(/font-size:\s*max\(1rem,\s*1em\)/);
    expect(rules).not.toMatch(/font-size:\s*1rem/);
  });

  test("sits in the base layer, so a step at or above 16px on the field itself still wins", () => {
    const layer = css.lastIndexOf("@layer base", css.indexOf(PHONE));
    expect(layer).toBeGreaterThanOrEqual(0);
    expect(blockFrom(layer)).toContain(PHONE);
    expect(css.indexOf(PHONE)).toBe(css.lastIndexOf(PHONE));
  });

  test("targets every field that takes typing", () => {
    for (const target of ["input:not(", "select", "textarea"]) expect(rules).toContain(target);
  });

  test("leaves checkboxes, radios, files and buttons at their own size", () => {
    for (const type of ["checkbox", "radio", "file", "button", "submit"]) {
      expect(rules).toContain(type);
    }
  });

  test("matches the narrow and wide breakpoint tokens", () => {
    expect(css).toContain("--breakpoint-narrow: 47.5rem");
    expect(css).toContain("--breakpoint-wide: 64rem");
  });
});

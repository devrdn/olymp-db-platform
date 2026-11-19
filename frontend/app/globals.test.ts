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

  // A dense mono field is `h-7`: its line at the desktop leading would be taller
  // than the box between its two borders, and the descenders would be cut.
  test("sets a data field's line short enough for its box at 16px", () => {
    const leading = /--text-data--line-height:\s*([\d.]+);/.exec(rules);
    expect(leading, "the data step's leading is set on a phone").not.toBeNull();
    const box = 28 - 2;

    expect(Number(leading![1]) * 16).toBeLessThanOrEqual(box);
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

describe("the overlay", () => {
  // The menu's dim and a dialog's backdrop are one surface, from one token.
  test("is a colour utility read from its token", () => {
    expect(block("@theme inline {")).toContain("--color-overlay: var(--overlay);");
  });
});

// The menu marks the root while its panel is open (menu-button.tsx).
describe("the page under an open menu", () => {
  test("cannot scroll, nor carry a pull past its edge to the browser", () => {
    const rules = block(":root[data-scroll-lock] {");

    expect(rules).toContain("overflow: hidden;");
    expect(rules).toContain("overscroll-behavior: none;");
  });

  test("keeps the room of the scrollbar it hides, only where one showed", () => {
    expect(block(':root[data-scroll-lock="keep-gutter"] {')).toContain("scrollbar-gutter: stable;");
  });
});

// jsdom cannot evaluate a media query, so this asserts the rule; the computed
// sizes outside and inside tables were measured in a real browser.
describe("small copy on a phone", () => {
  const PHONE_ONLY = "@media (width < 47.5rem) {";
  const rules = block(PHONE_ONLY);

  test("raises small, label and data to 14px below narrow", () => {
    for (const name of ["small", "label", "data"]) {
      expect(rules, name).toMatch(new RegExp(`--text-${name}:\\s*0\\.875rem;`));
    }
  });

  test("keeps a tabular figure and a drawn diagram at the data step's own size", () => {
    const keep = /([^{}]*)\{[^{}]*--text-data:\s*0\.8125rem;/.exec(rules);
    expect(keep, "a rule restores data to 13px").not.toBeNull();
    for (const selector of ["table", "svg"]) expect(keep![1]).toContain(selector);
  });

  test("leaves the field rule alone, so a field still draws 16px", () => {
    expect(rules).not.toContain("input");
    expect(block(PHONE)).toMatch(/--text-small:\s*1rem;/);
  });

  test("is a base-layer rule below narrow only", () => {
    const layer = css.lastIndexOf("@layer base", css.indexOf(PHONE_ONLY));
    expect(blockFrom(layer)).toContain(PHONE_ONLY);
    expect(css.indexOf(PHONE_ONLY)).toBe(css.lastIndexOf(PHONE_ONLY));
  });
});

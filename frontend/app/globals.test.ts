// @vitest-environment node
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, test } from "vitest";

const css = readFileSync(join(__dirname, "globals.css"), "utf8");

/** The body of the first top-level block opened by `header`. */
function block(header: string): string {
  const start = css.indexOf(header);
  expect(start, `${header} is in globals.css`).toBeGreaterThanOrEqual(0);
  let depth = 0;
  for (let i = css.indexOf("{", start); i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(css.indexOf("{", start) + 1, i);
  }
  throw new Error(`${header} is not closed`);
}

// jsdom cannot evaluate a media query, so this asserts the rule the phone
// relies on: iOS Safari zooms the page when a focused field is set under 16px.
describe("text entry on a phone", () => {
  const rules = block("@media (width < 47.5rem)");

  test("sets every field that takes typing to 1rem below the narrow breakpoint", () => {
    expect(rules).toMatch(/font-size:\s*1rem/);
    for (const target of ["input:not(", "select", "textarea"]) {
      expect(rules).toContain(target);
    }
  });

  test("leaves checkboxes, radios, files and buttons at their own size", () => {
    for (const type of ["checkbox", "radio", "file", "button", "submit"]) {
      expect(rules).toContain(type);
    }
  });

  test("sits outside every layer, so a utility such as text-control cannot undo it", () => {
    const layered = ["@layer base", "@layer utilities"].map(block);
    for (const layer of layered) expect(layer).not.toContain("font-size: 1rem");
    expect(css.indexOf("@media (width < 47.5rem)")).toBeGreaterThan(css.lastIndexOf("@layer utilities"));
  });

  test("matches the narrow breakpoint token", () => {
    expect(css).toContain("--breakpoint-narrow: 47.5rem");
  });
});

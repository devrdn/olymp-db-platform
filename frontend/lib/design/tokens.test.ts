import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * Every colour class a screen names must exist: Tailwind emits nothing for an
 * unknown class, and no build, type check or lint notices (SPEC.md §3.3).
 * The palette is read from the stylesheet and the source scanned against it.
 */

const ROOT = join(__dirname, "..", "..");

function defined(prefix: string): Set<string> {
  const css = readFileSync(join(ROOT, "app", "globals.css"), "utf8");
  const names = new Set<string>();
  for (const match of css.matchAll(new RegExp(`--${prefix}-([a-z0-9-]+?)(?:--[a-z-]+)?\\s*:`, "g"))) {
    names.add(match[1]);
  }
  return names;
}

/**
 * Words that follow a colour prefix without being colours. Listed rather than
 * inferred, so a typo cannot hide behind a rule.
 */
const NOT_A_COLOUR = new Set([
  "transparent", "current", "inherit", "white", "black", "none", "auto",
  "b", "t", "l", "r", "x", "y", "s", "e",
  "solid", "dashed", "dotted", "double", "hidden", "collapse", "separate", "clip",
  "left", "right", "center", "justify", "start", "end",
  "wrap", "nowrap", "balance", "pretty", "ellipsis",
  "cover", "contain", "fixed", "local", "scroll", "repeat", "no", "top", "bottom",
  // A CSS property named inside a style string, not a utility class.
  "color",
]);

function sourceFiles(dir: string, found: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === ".next" || entry.startsWith(".")) continue;
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) sourceFiles(path, found);
    // `.tsx` only: a `.ts` file may name `--text-...` inside a CSS string.
    else if (/\.tsx$/.test(entry) && !/\.test\.tsx$/.test(entry)) found.push(path);
  }
  return found;
}

/**
 * Print never gets the dark palette. Checked in the source, since jsdom cannot
 * be trusted with `@media print`: both dark rules must be gated to `screen`.
 */
describe("printing forces the light palette", () => {
  const css = readFileSync(join(ROOT, "styles", "tokens.css"), "utf8");

  it("gates the explicit dark theme to screen", () => {
    expect(css).toMatch(/@media screen\s*{\s*:root\[data-theme="dark"\]/);
  });

  it("gates the system dark preference to screen", () => {
    expect(css).toMatch(/@media screen and \(prefers-color-scheme: dark\)/);
  });
});

describe("the colours the interface names", () => {
  it("all exist in the design system", () => {
    const known = defined("color");
    const sizes = defined("text");
    const offenders: string[] = [];

    for (const file of [...sourceFiles(join(ROOT, "app")), ...sourceFiles(join(ROOT, "components"))]) {
      const source = readFileSync(file, "utf8");
      // The colour sits between the prefix and an optional opacity (`border-bad/40`).
      const pattern =
        /\b(?:bg|text|border|ring|divide|fill|stroke|outline|decoration|caret|shadow)-([a-z][a-z0-9-]*)(?:\/\d+)?\b/g;
      for (const match of source.matchAll(pattern)) {
        const name = match[1];
        if (known.has(name) || sizes.has(name) || NOT_A_COLOUR.has(name)) continue;
        // `border-l-2`, `border-b-0`: a side and a width, not a colour.
        if (/^[blrtxyse]-\d+$/.test(name)) continue;
        // A side and a colour (`border-l-gold`); the colour is still checked.
        const side = /^[blrtxyse]-([a-z][a-z0-9-]*)$/.exec(name);
        if (side && (known.has(side[1]) || NOT_A_COLOUR.has(side[1]))) continue;
        // `ring-offset-*` is a different utility.
        if (name.startsWith("offset-")) continue;
        // `bg-linear-to-r`: a gradient, not a colour.
        if (name.startsWith("linear-") || name.startsWith("radial-") || name.startsWith("conic-")) continue;
        offenders.push(`${file.slice(ROOT.length + 1)}: ${match[0]}`);
      }
    }

    expect(offenders).toEqual([]);
  });
});

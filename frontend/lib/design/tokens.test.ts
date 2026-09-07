import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * Every colour a screen names has to exist.
 *
 * Tailwind emits nothing for a class it cannot resolve, and nothing is not an
 * error — it is a rule that silently does not apply. Three invented colours
 * shipped on the play screen that way: `bg-danger` and `border-danger` made a
 * failed query look exactly like a "try again in a moment", and `bg-surface`
 * left both sticky table heads transparent, so rows scrolled through them.
 * None of it failed a build, a type check or a lint.
 *
 * So the palette is read from the stylesheet that defines it, and the source
 * is scanned for colour utilities naming anything else. SPEC.md §3.3 says
 * "no arbitrary colours"; this is what makes that checkable rather than a
 * matter of care.
 */

const ROOT = join(__dirname, "..", "..");

/** Names defined in the stylesheet under a given custom-property prefix. */
function defined(prefix: string): Set<string> {
  const css = readFileSync(join(ROOT, "app", "globals.css"), "utf8");
  const names = new Set<string>();
  for (const match of css.matchAll(new RegExp(`--${prefix}-([a-z0-9-]+?)(?:--[a-z-]+)?\\s*:`, "g"))) {
    names.add(match[1]);
  }
  return names;
}

/**
 * Words that follow a colour prefix without being colours: Tailwind's own
 * keywords, and the utilities that share the prefix — `border-b` is a side,
 * `bg-clip` is a behaviour. Listed rather than inferred, so a genuine typo
 * cannot hide behind a clever rule.
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
    // `.tsx` only: a className lives in markup, and a `.ts` file naming
    // `--text-body--line-height` inside a CSS string is not a class.
    else if (/\.tsx$/.test(entry) && !/\.test\.tsx$/.test(entry)) found.push(path);
  }
  return found;
}

describe("the colours the interface names", () => {
  it("all exist in the design system", () => {
    const known = defined("color");
    const sizes = defined("text");
    const offenders: string[] = [];

    for (const file of [...sourceFiles(join(ROOT, "app")), ...sourceFiles(join(ROOT, "components"))]) {
      const source = readFileSync(file, "utf8");
      // `bg-bad-wash`, `text-ink-3`, `border-bad/40` — the colour is what
      // stands between the prefix and an optional opacity.
      const pattern =
        /\b(?:bg|text|border|ring|divide|fill|stroke|outline|decoration|caret|shadow)-([a-z][a-z0-9-]*)(?:\/\d+)?\b/g;
      for (const match of source.matchAll(pattern)) {
        const name = match[1];
        if (known.has(name) || sizes.has(name) || NOT_A_COLOUR.has(name)) continue;
        // `border-l-2`, `border-b-0`: a side and a width, not a colour.
        if (/^[blrtxyse]-\d+$/.test(name)) continue;
        // `ring-offset-2`, `ring-offset-bg`: a different utility that happens
        // to share the prefix.
        if (name.startsWith("offset-")) continue;
        // `bg-linear-to-r`: a gradient, not a colour.
        if (name.startsWith("linear-") || name.startsWith("radial-") || name.startsWith("conic-")) continue;
        offenders.push(`${file.slice(ROOT.length + 1)}: ${match[0]}`);
      }
    }

    expect(offenders).toEqual([]);
  });
});

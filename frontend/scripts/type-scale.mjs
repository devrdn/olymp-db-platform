#!/usr/bin/env node
/**
 * Every `text-*` class, checked against the scale and the palette that exist.
 *
 * The stock Tailwind ladder is erased in `globals.css` — `--text-*: initial`
 * — so that reaching for a size means picking a step the system defines. What
 * that erasure does not do is complain: a class naming a step that was never
 * defined simply produces no CSS. The element keeps whatever it inherited, the
 * page still renders, and nothing anywhere says a word.
 *
 * That is not hypothetical. `text-h1` was written on two headings, and both
 * shipped at body size — 17px where 52px was intended — through a type check,
 * a lint, a build and a screenshot review, because a heading at the wrong size
 * still looks like a heading until it is beside a right one.
 *
 * A `text-*` class is one of three things, and all three are checked here: a
 * step of the type scale, a colour from the palette, or one of the handful of
 * Tailwind utilities that happen to share the prefix without being either.
 *
 * Run from the frontend directory.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const STYLES = "app/globals.css";
const ROOTS = ["app", "components"];

/**
 * Tailwind's own `text-*` utilities that set neither a size nor a colour.
 * Alignment, wrapping and overflow all live under the same prefix.
 */
const NOT_A_SCALE_STEP = new Set([
  "left",
  "center",
  "right",
  "justify",
  "start",
  "end",
  "wrap",
  "nowrap",
  "balance",
  "pretty",
  "ellipsis",
  "clip",
  "current",
  "inherit",
  "transparent",
]);

function namesOf(css, prefix) {
  const found = new Set();
  for (const [, name] of css.matchAll(new RegExp(`^\\s*--${prefix}-([a-z0-9-]+):`, "gm"))) {
    // `--text-h3--line-height` declares a property of a step, not a step.
    if (!name.includes("--")) found.add(name);
  }
  return found;
}

const css = readFileSync(STYLES, "utf8");
const steps = namesOf(css, "text");
const colours = namesOf(css, "color");

function sources(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) out.push(...sources(path));
    else if (/\.(tsx|ts)$/.test(entry) && !entry.endsWith(".test.tsx")) out.push(path);
  }
  return out;
}

const unknown = new Map();

for (const file of ROOTS.flatMap(sources)) {
  const source = readFileSync(file, "utf8");

  // Only inside a class string, and only whole classes: a `text-` inside a
  // translated sentence or an identifier is not a utility.
  for (const [, name] of source.matchAll(/(?:^|[\s"'`])text-([a-z][a-z0-9-]*)(?=[\s"'`]|$)/gm)) {
    if (steps.has(name) || colours.has(name) || NOT_A_SCALE_STEP.has(name)) continue;

    const where = unknown.get(name) ?? new Set();
    where.add(file);
    unknown.set(name, where);
  }
}

if (unknown.size === 0) {
  console.log(
    `Every text-* class names one of the ${steps.size} scale steps or ${colours.size} colours.`,
  );
  process.exit(0);
}

for (const [name, where] of [...unknown].sort()) {
  console.error(`unknown   text-${name}  — no such scale step or colour`);
  for (const file of where) console.error(`            ${file}`);
}
console.error(`\nThe scale is: ${[...steps].sort().join(", ")}`);
process.exit(1);

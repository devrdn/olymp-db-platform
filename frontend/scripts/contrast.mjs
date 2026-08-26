#!/usr/bin/env node
/**
 * Checks the palette against WCAG, from the tokens rather than from a
 * screenshot.
 *
 * Two thresholds, because the spec keeps two apart (section 3.2): body text
 * needs 4.5:1 (SC 1.4.3) and the boundary of an interactive control needs
 * 3:1 (SC 1.4.11), while a decorative rule is held to nothing. A single
 * "border colour" would have to satisfy the stricter of the two or quietly
 * fail it, which is what happened in two drafts before `--edge` was split off
 * from `--line`.
 *
 * This runs in CI because the failures it catches are invisible: muted text at
 * 2.4:1 and a field border at 1.49:1 both look fine to someone who already
 * knows what they say.
 *
 *   node scripts/contrast.mjs
 */

import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const TOKENS = join(dirname(fileURLToPath(import.meta.url)), "..", "styles", "tokens.css");

const TEXT = 4.5;
const CONTROL = 3;

/**
 * Every pair that carries meaning, and the threshold it answers to.
 *
 * A pair that is not here is not checked, so adding a token means adding its
 * row: the list is the claim, and an unlisted colour is an unverified one.
 */
const PAIRS = [
  ["ink", "bg", TEXT],
  ["ink-2", "bg", TEXT],
  ["ink-3", "bg", TEXT],
  ["ink", "panel", TEXT],
  ["ink-2", "panel", TEXT],
  ["ink-3", "panel", TEXT],
  ["ink", "sunk", TEXT],
  ["ink-2", "sunk", TEXT],
  ["ink-3", "sunk", TEXT],
  ["cta-fg", "cta-bg", TEXT],
  ["accent-ink", "bg", TEXT],
  ["accent-ink", "accent-wash", TEXT],
  ["good", "good-wash", TEXT],
  ["warn", "warn-wash", TEXT],
  ["bad", "bad-wash", TEXT],
  ["bad", "bg", TEXT],
  // The boundary of an interactive control, and the focus ring that lands on
  // whichever surface the control sits on.
  ["edge", "bg", CONTROL],
  ["edge", "panel", CONTROL],
  ["edge", "sunk", CONTROL],
  ["accent-ink", "panel", CONTROL],
];

/**
 * The one documented exception: the annotation caption sits
 * near 1.6:1 by design and may never be the only thing carrying a fact. It is
 * listed so that nobody "fixes" it, and so that its value cannot drift into
 * looking like real text.
 */
const EXEMPT = [["ann", "bg", "decorative annotation, never the sole carrier of information"]];

function parse(css) {
  const light = new Map();
  const dark = new Map();

  for (const [, body] of css.matchAll(/\{([^{}]*)\}/g)) {
    for (const [, name, value] of body.matchAll(/--([\w-]+)\s*:\s*([^;]+);/g)) {
      // The theme blocks assign `--bg: var(--dark-bg)`; the values themselves
      // live once, under their own `--dark-` names.
      if (value.includes("var(")) continue;
      if (name.startsWith("dark-")) dark.set(name.slice(5), value.trim());
      else if (!light.has(name)) light.set(name, value.trim());
    }
  }

  return { light, dark };
}

function channels(value) {
  const hex = value.match(/^#([0-9a-f]{6})$/i);
  if (hex) {
    const n = parseInt(hex[1], 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1];
  }

  const rgb = value.match(
    /^rgba?\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)(?:\s*[/,]\s*([\d.]+))?\s*\)$/i,
  );
  if (rgb) {
    return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3]), rgb[4] === undefined ? 1 : Number(rgb[4])];
  }

  throw new Error(`Cannot read the colour ${value}`);
}

/** A translucent token is only ever seen over the page ground; measure it there. */
function flatten(value, ground) {
  const [r, g, b, a] = channels(value);
  if (a === 1) return [r, g, b];
  const [br, bg, bb] = channels(ground);
  return [r * a + br * (1 - a), g * a + bg * (1 - a), b * a + bb * (1 - a)];
}

function luminance([r, g, b]) {
  const [rr, gg, bb] = [r, g, b].map((c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * rr + 0.7152 * gg + 0.0722 * bb;
}

function ratio(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m);
  return (x + 0.05) / (y + 0.05);
}

const { light, dark } = parse(await readFile(TOKENS, "utf8"));

const themes = [
  ["light", (name) => light.get(name)],
  ["dark", (name) => dark.get(name) ?? light.get(name)],
];

let failures = 0;

for (const [theme, get] of themes) {
  const ground = get("bg");
  console.log(`\n  ${theme}`);

  for (const [fg, bgName, threshold] of PAIRS) {
    const fgValue = get(fg);
    const bgValue = get(bgName);
    if (!fgValue || !bgValue) {
      console.log(`  ??  --${fg} on --${bgName}: token missing`);
      failures += 1;
      continue;
    }

    const value = ratio(flatten(fgValue, ground), flatten(bgValue, ground));
    const ok = value >= threshold;
    if (!ok) failures += 1;
    console.log(
      `  ${ok ? "ok" : "NO"}  --${fg} on --${bgName}  ${value.toFixed(2)}:1  (needs ${threshold})`,
    );
  }

  for (const [fg, bgName, why] of EXEMPT) {
    const value = ratio(flatten(get(fg), ground), flatten(get(bgName), ground));
    console.log(`  --  --${fg} on --${bgName}  ${value.toFixed(2)}:1  exempt: ${why}`);
  }
}

if (failures > 0) {
  console.error(`\n${failures} contrast check(s) failed.\n`);
  process.exit(1);
}

console.log("\nEvery checked pair clears its threshold.\n");

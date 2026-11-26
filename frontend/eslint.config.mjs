import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

import { noSchemaInClient } from "./scripts/no-schema-in-client.mjs";

/**
 * The three rules that keep the design system from being decorative.
 *
 * A design system is a claim that every colour, every type step and every
 * weight in the product came from one place. Nothing enforces that claim by
 * itself: one `text-[13px]` under a deadline is invisible in review, and a
 * month of them is a system nobody trusts. These turn each of the three into a
 * build failure, which is the only review that never gets tired.
 *
 * Each pattern is checked in plain strings and in template literals, because
 * `className={`... ${x}`}` is where they hide.
 */
const BANNED = [
  {
    // Spec section 3.3. `tokens.css` is the only place a colour is written.
    pattern:
      "(?:bg|text|border|outline|ring|fill|stroke|shadow|decoration|caret|accent|divide|placeholder|from|via|to)-\\[(?:#|rgb|hsl|oklch|lab|color-mix)",
    message:
      "Arbitrary colour. Every colour comes from styles/tokens.css: use a token utility (bg-panel, text-ink-2, border-edge).",
  },
  {
    // Spec section 4. The scale carries size, leading, tracking and weight
    // together, and taking one without the others is how nine steps became a
    // hand-written size in every file.
    pattern: "\\b(?:text|tracking|leading)-\\[",
    message:
      "Arbitrary type. Use a step from the scale (text-display, text-h2, text-body, text-label, text-data). A tenth step is an edit to @theme in app/globals.css.",
  },
  {
    // Spec section 15: there is no 700 in this system. Hierarchy is carried by
    // scale, and a heavy large grotesque on Cyrillic reads as a palisade.
    pattern: "\\bfont-(?:bold|extrabold|black)\\b",
    message:
      "There is no weight 700 in this system. Hierarchy comes from scale; font-semibold is the ceiling and belongs to the primary action.",
  },
];

const restricted = BANNED.flatMap(({ pattern, message }) => [
  { selector: `Literal[value=/${pattern}/]`, message },
  { selector: `TemplateElement[value.raw=/${pattern}/]`, message },
]);

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    rules: {
      "no-restricted-syntax": ["error", ...restricted],
    },
  },
  {
    files: ["**/*.ts", "**/*.tsx"],
    plugins: { bundle: { rules: { "no-schema-in-client": noSchemaInClient } } },
    rules: {
      "bundle/no-schema-in-client": ["error", { root: import.meta.dirname }],
    },
  },
  {
    // The content policy guard in e2e/support.ts, which a check cannot skip.
    files: ["e2e/**/*.{spec,perf}.ts"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          paths: [
            {
              name: "@playwright/test",
              importNames: ["test"],
              message: "Import `test` from ./support, which watches the content policy.",
            },
          ],
        },
      ],
      // Replaces the list above for these files, so it repeats it.
      "no-restricted-syntax": [
        "error",
        ...restricted,
        {
          // `browser.newContext` and `….browser.newContext`; an API request context is fine.
          selector:
            "CallExpression[callee.property.name='newContext']:matches([callee.object.name='browser'], [callee.object.property.name='browser'])",
          message: "Open a context with newContext from ./support, which watches the content policy.",
        },
      ],
    },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
    // Playwright's run output.
    "test-results/**",
    "playwright-report/**",
  ]),
]);

export default eslintConfig;

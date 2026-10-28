/**
 * The story PDF's typography, as the screen sets the story (`StoryText`'s
 * `PROSE`), converted to paper by one ratio. `theme.test.ts` holds every value
 * here to `app/globals.css`, `styles/tokens.css` and the font files.
 */

/**
 * Points per rem: the one conversion from screen to paper. It puts the
 * narrative body (1.125rem) at 11.25 pt, a comfortable size for a printed
 * handout, and every other size and distance keeps its proportion to it.
 */
export const PT_PER_REM = 10;

export const pt = (rem: number) => rem * PT_PER_REM;

/** A CSS pixel, which the screen uses for rules and paints its markers in. */
export const PX = pt(1 / 16);

/** A4 in points, from ISO 216 (210 x 297 mm). */
export const A4 = { width: 595.28, height: 841.89 } as const;

/** The families, by the names the PDF library knows them under. */
export type Face = "Literata" | "Onest" | "JetBrainsMono";

/**
 * Each face's ascender minus its descender, in em: the height the library
 * gives a line before its line height multiplies it, and the content area CSS
 * centres a line's text in.
 */
export const NATURAL: Record<Face, number> = { Literata: 1.485, Onest: 1.275, JetBrainsMono: 1.32 };

/** Each face's ascender, in em: where the library puts the baseline below a line's top. */
export const ASCENDER: Record<Face, number> = { Literata: 1.177, Onest: 0.97, JetBrainsMono: 1.02 };

/** A step of the type scale: size in pt, line height as CSS gives it (times the size), tracking in em. */
export type Step = { face: Face; size: number; leading: number; tracking: number; medium: boolean };

/** The library's line height for a step: CSS's line box over the face's own height. */
export const lineHeight = (step: Step) => step.leading / NATURAL[step.face];

/**
 * How much lower CSS sets a line's text in its line box than the library does:
 * half the leading. CSS centres the text in the box; the library sets it at
 * the top and puts all the extra leading below.
 */
export const inset = (step: Step) => ((step.leading - NATURAL[step.face]) / 2) * step.size;

/**
 * `text-h2` is `clamp(2rem, 3.6vw, 3.25rem)`. Paper takes the clamp with the
 * sheet as the viewport: A4 is 952 px wide at the ratio above, which gives
 * 2.14rem, near the low end, the size a column this narrow has on screen.
 */
const SHEET_PX = A4.width / PX;
const H2_REM = Math.min(Math.max(2, (0.036 * SHEET_PX) / 16), 3.25);

/** The steps `StoryText` uses, in the face it uses each in. */
export const STEP = {
  // Body text, `text-narrative`, in Literata.
  narrative: { face: "Literata", size: pt(1.125), leading: 1.7, tracking: 0, medium: false },
  // The story's `#` and the contest's title, `text-h2`.
  h2: { face: "Onest", size: pt(H2_REM), leading: 1.02, tracking: -0.042, medium: false },
  // The story's `##`, `text-h3`.
  h3: { face: "Onest", size: pt(1.3125), leading: 1.3, tracking: -0.02, medium: true },
  // The story's `###`, `text-row`.
  row: { face: "Onest", size: pt(1.09375), leading: 1.35, tracking: -0.022, medium: true },
  // Table cells, `text-body`.
  body: { face: "Onest", size: pt(1.0625), leading: 1.65, tracking: 0, medium: false },
  // Table headers, `text-label`, uppercase. The token's weight is 500; the
  // mono face is embedded in its regular weight only.
  label: { face: "JetBrainsMono", size: pt(0.78125), leading: 1.5, tracking: 0.1, medium: false },
  // Page numbers, `text-small`.
  small: { face: "Onest", size: pt(0.875), leading: 1.5, tracking: 0, medium: false },
} as const satisfies Record<string, Step>;

/** Inline code and code blocks, `text-data`, in the mono face. */
export const DATA_SIZE = pt(0.8125);

/** The distances `PROSE` writes, as Tailwind's spacing scale gives them (a step is 0.25rem). */
export const SPACE = {
  block: pt(1), // mt-4, between the story's blocks
  h2: pt(2), // mt-8, before a `#`
  h3: pt(1.75), // mt-7, before a `##`
  row: pt(1.5), // mt-6, before a `###`
  item: pt(0.375), // mt-1.5, before each list item
  listIndent: pt(1.25), // pl-5
  quoteIndent: pt(1), // pl-4
  quoteRule: 2 * PX, // border-l-2
  codePad: pt(0.75), // p-3 on a code block
  cellY: pt(0.375), // py-1.5
  cellRight: pt(1), // pr-4
  rule: PX, // a 1px border or rule
  titleAfter: pt(2), // mb-8 below the title, as the participant's print copy has it
} as const;

/** A millimetre in points. */
export const MM = 72 / 25.4;

/** The margin on each side of the text, and below it. */
const MARGIN = 20 * MM;

/**
 * The text column. Paper sets its own measure rather than the screen's 58ch,
 * so that a story takes fewer sheets; the type and the spacing still come from
 * the screen.
 */
export const MEASURE = A4.width - 2 * MARGIN;

/** The light theme: paper is white whatever theme the administrator uses. */
export const COLOR = {
  ink: "#0d1117",
  ink2: "#4c535e",
  ink3: "#66696d",
  sunk: "#f6f6f6",
  accent: "#0e5048",
  paper: "#ffffff",
  // `--line`, `--line-2` and `--ann` are ink at an alpha; on white paper they are these.
  line: "#eeeeef",
  line2: "#e0e0e1",
  ann: "#c5c6c7",
} as const;

/**
 * The logo as the running header prints it on every page, far larger than the
 * app bar's `h-5`: a printed page is read from arm's length, and the header
 * carries the installation's identity on every sheet. An uploaded logo fits
 * inside the box keeping its proportions; the product mark is an ink square of
 * the box's height with the mark at the app bar's three fifths of it (`size-3`
 * in `size-5`).
 */
export const LOGO = { height: 28, maxWidth: 180, mark: (28 * 3) / 5 } as const;

const BOTTOM = MARGIN;
// The page number's top sits this far below the text block.
const FOOTER = pt(2);

/**
 * The running header mirrors the footer: the logo's box stands as far from
 * the sheet's top edge as the page number's line from its bottom edge.
 */
export const HEADER = {
  top: BOTTOM - FOOTER - NATURAL[STEP.small.face] * STEP.small.size,
  titleGap: pt(0.875), // gap-3.5, the app bar's gap between its groups
  ruleGap: pt(0.875), // the app bar's room around its logo, (h-12 - h-5) / 2
} as const;

/** Paper's own margins, which the screen has no counterpart for. */
export const PAGE = {
  side: MARGIN,
  // The header, its rule, and the footer's distance again before the text.
  top: HEADER.top + LOGO.height + HEADER.ruleGap + SPACE.rule + FOOTER,
  bottom: BOTTOM,
  footer: FOOTER,
} as const;

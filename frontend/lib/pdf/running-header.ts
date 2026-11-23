import { CAT, markSvg } from "@/lib/brand/marks.mjs";

import type { LogoImage } from "./logo";
import { COLOR, HEADER, LOGO, MEASURE, NATURAL, PAGE, SPACE, STEP } from "./theme";
import type { Node } from "./types";

/** The uploaded logo, drawn; `"mark"` for the product mark; `null` for none. */
export type Logo = LogoImage | "mark" | null;

/** The product's cat (`lib/brand/marks.mjs`), standing the logo box's full height. */
function productMark(): Node & { width: number } {
  const [, , width, height] = CAT.viewBox.split(" ").map(Number);
  return {
    svg: markSvg(CAT, { ink: COLOR.logoInk, chalk: COLOR.logoChalk }),
    width: (LOGO.height * width) / height,
    height: LOGO.height,
  };
}

/** The logo's cell in the header's row, centred in the row's height. */
function logoCell(logo: LogoImage | "mark"): Node & { width: number } {
  if (logo === "mark") return productMark();
  const scale = Math.min(LOGO.height / logo.height, LOGO.maxWidth / logo.width);
  const [width, height] = [logo.width * scale, logo.height * scale];
  const room = (LOGO.height - height) / 2;
  return { image: "logo", width, height, margin: [0, room, 0, room] };
}

/** The header's title, `text-small` in ink-2; `fitTitle` measures in it. */
export const HEADER_TITLE = { font: STEP.small.face, fontSize: STEP.small.size, color: COLOR.ink2 } as const;

/** The width the header leaves the title, beside the logo, on a text column `width` wide. */
export function titleSpace(logo: Logo, width: number = MEASURE): number {
  return logo ? width - logoCell(logo).width - HEADER.titleGap : width;
}

/**
 * The style every node of the running header and footer carries. It names no
 * style, so it changes nothing drawn; it tells them apart from the body in
 * `pageBreakBefore`, since the library lists them among a page's nodes.
 */
const RUNNING = "running";

/** Marks a node and its `stack` and `columns` children; a header or footer built with another container needs it here too. */
export function running(node: Node): Node {
  const marked: Node = { ...node, style: RUNNING };
  for (const key of ["stack", "columns"]) if (Array.isArray(node[key])) marked[key] = (node[key] as Node[]).map(running);
  return marked;
}

/** Whether a node the library lists on a page belongs to the body, not to the header or footer. */
export const ofBody = (node: { style?: unknown }) => node.style !== RUNNING;

/** The page number, "3 / 12", centred; the caller places it. */
export function pageNumber(page: number, pages: number): Node {
  return { text: `${page} / ${pages}`, alignment: "center", font: STEP.small.face, fontSize: STEP.small.size, color: COLOR.ink3 };
}

/** The footer both documents' simple pages carry: the page number, in the text column's margins. */
export function pageFooter(page: number, pages: number): Node {
  return { ...pageNumber(page, pages), margin: [PAGE.side, PAGE.footer, PAGE.side, 0] };
}

/**
 * The running header: the logo, the title on its right, and a rule below
 * spanning the text column, `width` wide. Built afresh for each page, since
 * the library consumes the nodes it lays out.
 */
export function runningHeader(logo: Logo, title: string, width: number = MEASURE): Node {
  // The title's line centred on the row, as `items-center` centres it.
  const room = (LOGO.height - NATURAL[HEADER_TITLE.font] * HEADER_TITLE.fontSize) / 2;
  const titleCell = { text: title, ...HEADER_TITLE, alignment: "right", width: "*", margin: [0, room, 0, room] };
  const rule = SPACE.rule / 2;
  return {
    margin: [PAGE.side, HEADER.top, PAGE.side, 0],
    stack: [
      { columns: logo ? [logoCell(logo), titleCell] : [titleCell], columnGap: HEADER.titleGap },
      {
        canvas: [{ type: "line", x1: 0, y1: rule, x2: width, y2: rule, lineWidth: SPACE.rule, lineColor: COLOR.line }],
        margin: [0, HEADER.ruleGap, 0, 0],
      },
    ],
  };
}

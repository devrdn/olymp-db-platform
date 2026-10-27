import type { LogoImage } from "./logo";
import { storyBlocks, tracked, type Node } from "./story-markdown";
import { COLOR, PAGE, SPACE, STEP, inset, lineHeight, pt } from "./theme";

/**
 * The logo stands as the app bar shows it: `h-5`, at most `max-w-32` wide, or
 * the product mark in its `size-5` ink square with the mark `size-3` inside.
 */
const LOGO = { height: pt(1.25), maxWidth: pt(8), mark: pt(0.75) } as const;

/** The product mark (`components/layout/mark.tsx`), on a 16-unit grid. */
export const MARK_PATH = "M1 2h5.6v1.6H15V14H1V2Zm3 5.2h8v1.2H4V7.2Zm0 3h5.6v1.2H4v-1.2Z";

/** The mark's outlines: the card first, then the two rules cut out of it. Reads `M`, `m`, `H`, `h`, `V`, `v` and `Z`. */
export function markOutlines(path: string): { x: number; y: number }[][] {
  const outlines: { x: number; y: number }[][] = [];
  let x = 0;
  let y = 0;
  let start = { x, y };
  for (const [, command, number] of path.matchAll(/([MmHhVvZz])\s*([-\d.]*(?:\s+[-\d.]+)?)/g)) {
    const [a, b] = number.trim().split(/\s+/).map(Number);
    switch (command) {
      case "M":
      case "m":
        [x, y] = command === "M" ? [a, b] : [x + a, y + b];
        start = { x, y };
        outlines.push([{ x, y }]);
        continue;
      case "H":
        x = a;
        break;
      case "h":
        x += a;
        break;
      case "V":
        y = a;
        break;
      case "v":
        y += a;
        break;
      default:
        ({ x, y } = start);
        continue;
    }
    outlines[outlines.length - 1].push({ x, y });
  }
  return outlines;
}

/** The mark drawn as the app bar draws it: the paper-coloured card on an ink square, its rules in ink. */
function productMark(): Node {
  const scale = LOGO.mark / 16;
  const offset = (LOGO.height - LOGO.mark) / 2;
  const [card, ...rules] = markOutlines(MARK_PATH).map((outline) =>
    outline.map((point) => ({ x: offset + point.x * scale, y: offset + point.y * scale })),
  );
  return {
    canvas: [
      { type: "rect", x: 0, y: 0, w: LOGO.height, h: LOGO.height, color: COLOR.ink },
      { type: "polyline", points: card, closePath: true, color: COLOR.paper, lineWidth: 0 },
      ...rules.map((points) => ({ type: "polyline", points, closePath: true, color: COLOR.ink, lineWidth: 0 })),
    ],
  };
}

function uploadedLogo(logo: LogoImage): Node {
  const scale = Math.min(LOGO.height / logo.height, LOGO.maxWidth / logo.width);
  return { image: "logo", width: logo.width * scale, height: logo.height * scale };
}

export type StoryDocumentInput = {
  title: string;
  /** The story's language code, which the file declares. */
  language: string;
  markdown: string;
  /** The uploaded logo, drawn; `"mark"` for the product mark; `null` for none. */
  logo: LogoImage | "mark" | null;
  /** One list of column widths per table, from measuring them. */
  columnWidths?: number[][];
};

/**
 * The page: the logo, the contest's title in the story's `#` style, the story,
 * and page numbers; nothing else of the contest is printed. Returns the
 * library's document definition and the story's tables, for the caller to
 * measure.
 */
export function storyDocument({ title, language, markdown, logo, columnWidths }: StoryDocumentInput) {
  const story = storyBlocks(markdown, columnWidths);
  const titleStep = STEP.h2;

  const logoNode = logo === "mark" ? productMark() : logo ? uploadedLogo(logo) : null;
  // Below the title, the larger of its own `mb-8` and the story's first margin, as on the screen.
  const storyGap = Math.max(SPACE.titleAfter, story.mt) + story.first - inset(titleStep);

  const definition: Node = {
    info: { title, creator: "DB Contest", producer: "DB Contest" },
    language,
    pageSize: "A4",
    pageMargins: [PAGE.side, PAGE.top, PAGE.side, PAGE.bottom],
    defaultStyle: { font: "Literata", fontSize: STEP.narrative.size, color: COLOR.ink },
    images: logo && logo !== "mark" ? { logo: logo.src } : {},
    content: [
      ...(logoNode ? [{ ...logoNode, margin: [0, 0, 0, SPACE.titleAfter] }] : []),
      {
        text: tracked({ text: title }, titleStep.tracking * titleStep.size),
        font: titleStep.face,
        fontSize: titleStep.size,
        lineHeight: lineHeight(titleStep),
        characterSpacing: titleStep.tracking * titleStep.size,
        color: COLOR.ink,
        margin: [0, inset(titleStep), 0, 0],
      },
      { stack: story.content, margin: [0, storyGap, 0, 0] },
    ],
    footer: (page: number, pages: number) => ({
      text: `${page} / ${pages}`,
      alignment: "center",
      font: STEP.small.face,
      fontSize: STEP.small.size,
      color: COLOR.ink3,
      margin: [PAGE.side, PAGE.footer, PAGE.side, 0],
    }),
    // A heading is never the last thing on a page, apart from what it heads.
    pageBreakBefore: (node: { headlineLevel?: number }, nodes: { getFollowingNodesOnPage(): unknown[] }) =>
      Boolean(node.headlineLevel) && nodes.getFollowingNodesOnPage().length === 0,
  };
  return { definition, tables: story.tables };
}

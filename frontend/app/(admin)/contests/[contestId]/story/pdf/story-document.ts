import { ofBody, pageFooter, running, runningHeader, type Logo } from "@/lib/pdf/running-header";
import { COLOR, PAGE, SPACE, STEP, inset, lineHeight } from "@/lib/pdf/theme";
import type { Node } from "@/lib/pdf/types";

import { storyBlocks, tracked } from "./story-markdown";

/** What the library tells `pageBreakBefore` of a laid-out node. */
type NodeInfo = { headlineLevel?: number; style?: unknown };

/** The nodes around the one `pageBreakBefore` is asked about, in document order. */
type PageNodes = { getFollowingNodesOnPage(): NodeInfo[]; getNodesOnNextPage(): NodeInfo[] };

/**
 * What a heading must share its page with: the story's text, not another
 * heading. The library asks about each node once, in order, so a run of
 * headings at a page's foot has to move at its first heading.
 */
const text = (node: NodeInfo) => ofBody(node) && !node.headlineLevel;

export type StoryDocumentInput = {
  title: string;
  /** The story's language code, which the file declares. */
  language: string;
  markdown: string;
  /** The uploaded logo, drawn; `"mark"` for the product mark; `null` for none. */
  logo: Logo;
  /** One list of column widths per table, from measuring them. */
  columnWidths?: number[][];
  /** The title as the header prints it, cut by `fitTitle` to `titleSpace`; the whole title when absent. */
  headerTitle?: string;
};

/**
 * The pages: a running header with the logo and the contest's title, the
 * title again in the story's `#` style as the document's heading, the story,
 * and page numbers; nothing else of the contest is printed. Returns the
 * library's document definition and the story's tables, for the caller to
 * measure.
 */
export function storyDocument({ title, language, markdown, logo, columnWidths, headerTitle = title }: StoryDocumentInput) {
  const story = storyBlocks(markdown, columnWidths);
  const titleStep = STEP.h2;
  // Below the title, the larger of its own `mb-8` and the story's first margin, as on the screen.
  const storyGap = Math.max(SPACE.titleAfter, story.mt) + story.first - inset(titleStep);

  const definition: Node = {
    info: { title, creator: "OlympDB Platform", producer: "OlympDB Platform" },
    language,
    pageSize: "A4",
    pageMargins: [PAGE.side, PAGE.top, PAGE.side, PAGE.bottom],
    defaultStyle: { font: "Literata", fontSize: STEP.narrative.size, color: COLOR.ink },
    images: logo && logo !== "mark" ? { logo: logo.src } : {},
    header: () => running(runningHeader(logo, headerTitle)),
    content: [
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
    footer: (page: number, pages: number) => running(pageFooter(page, pages)),
    // A heading is never the last thing on a page, apart from what it heads.
    pageBreakBefore: (node: NodeInfo, nodes: PageNodes) =>
      Boolean(node.headlineLevel) && !nodes.getFollowingNodesOnPage().some(text) && nodes.getNodesOnNextPage().some(ofBody),
  };
  return { definition, tables: story.tables };
}

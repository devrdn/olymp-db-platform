import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, test } from "vitest";

import { MARK_PATH, markOutlines, storyDocument } from "./story-document";
import type { Node } from "./story-markdown";
import { A4, COLOR, MEASURE, PAGE, STEP } from "./theme";

const input = { title: "Crima din seră", language: "ro", markdown: "Ștefan a venit." };

function content(definition: Node): Node[] {
  return definition.content as Node[];
}

describe("the product mark", () => {
  test("is the mark the app draws, path for path", () => {
    const component = readFileSync(join(process.cwd(), "components/layout/mark.tsx"), "utf8");
    const icon = readFileSync(join(process.cwd(), "public/icon.svg"), "utf8");

    expect(component).toContain(`d="${MARK_PATH}"`);
    expect(icon).toContain(`d="${MARK_PATH}"`);
  });

  test("reads as the card with its tab, and the two rules cut out of it", () => {
    const rounded = markOutlines(MARK_PATH).map((outline) => outline.map(({ x, y }) => ({ x: +x.toFixed(6), y: +y.toFixed(6) })));

    expect(rounded).toEqual([
      [
        { x: 1, y: 2 },
        { x: 6.6, y: 2 },
        { x: 6.6, y: 3.6 },
        { x: 15, y: 3.6 },
        { x: 15, y: 14 },
        { x: 1, y: 14 },
        { x: 1, y: 2 },
      ],
      [
        { x: 4, y: 7.2 },
        { x: 12, y: 7.2 },
        { x: 12, y: 8.4 },
        { x: 4, y: 8.4 },
        { x: 4, y: 7.2 },
      ],
      [
        { x: 4, y: 10.2 },
        { x: 9.6, y: 10.2 },
        { x: 9.6, y: 11.4 },
        { x: 4, y: 11.4 },
        { x: 4, y: 10.2 },
      ],
    ]);
  });

  test("is the app bar's mark set larger: an ink square of h-8 with the card in paper and the rules in ink", () => {
    const [mark] = content(storyDocument({ ...input, logo: "mark" }).definition);
    const [square, card, ...rules] = mark.canvas as Node[];

    expect(square).toMatchObject({ type: "rect", w: 20, h: 20, color: COLOR.ink });
    expect(card).toMatchObject({ type: "polyline", closePath: true, color: COLOR.paper });
    expect(rules.map((rule) => rule.color)).toEqual([COLOR.ink, COLOR.ink]);
  });
});

describe("storyDocument", () => {
  test("an uploaded logo is fitted into a box h-8 high and at most max-w-52 wide", () => {
    const tall = content(storyDocument({ ...input, logo: { src: "data:image/jpeg;base64,", width: 100, height: 200 } }).definition)[0];
    const wide = content(storyDocument({ ...input, logo: { src: "data:image/jpeg;base64,", width: 1000, height: 100 } }).definition)[0];

    expect(tall).toMatchObject({ image: "logo", width: 10, height: 20 });
    expect(wide).toMatchObject({ image: "logo", width: 130, height: 13 });
  });

  test("without a logo the page begins with the title", () => {
    const [title] = content(storyDocument({ ...input, logo: null }).definition);

    expect(title).toMatchObject({ font: "Onest", fontSize: STEP.h2.size, color: COLOR.ink });
  });

  test("is A4, its column the narrative measure centred on the sheet, in the story's language", () => {
    const { definition } = storyDocument({ ...input, logo: null });
    const [left, , right] = definition.pageMargins as number[];

    expect(definition).toMatchObject({ pageSize: "A4", language: "ro", info: { title: "Crima din seră" } });
    expect(left).toBeCloseTo(right, 6);
    expect(A4.width - left - right).toBeCloseTo(MEASURE, 6);
  });

  test("numbers its pages small, in ink-3", () => {
    const footer = storyDocument({ ...input, logo: null }).definition.footer as (page: number, pages: number) => Node;

    expect(footer(2, 5)).toMatchObject({ text: "2 / 5", font: "Onest", fontSize: STEP.small.size, color: COLOR.ink3, margin: [PAGE.side, PAGE.footer, PAGE.side, 0] });
  });

  test("never leaves a heading as the last thing on a page", () => {
    const breakBefore = storyDocument({ ...input, logo: null }).definition.pageBreakBefore as (
      node: { headlineLevel?: number },
      nodes: { getFollowingNodesOnPage(): unknown[] },
    ) => boolean;
    const followedBy = (count: number) => ({ getFollowingNodesOnPage: () => Array.from({ length: count }) });

    expect(breakBefore({ headlineLevel: 2 }, followedBy(0))).toBe(true);
    expect(breakBefore({ headlineLevel: 2 }, followedBy(1))).toBe(false);
    expect(breakBefore({}, followedBy(0))).toBe(false);
  });

  test("reports the story's tables for measuring", () => {
    expect(storyDocument({ ...input, markdown: "| a |\n|---|\n| 1 |", logo: null }).tables).toHaveLength(1);
  });
});

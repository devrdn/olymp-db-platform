import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, test } from "vitest";

import { MARK_PATH, markOutlines, storyDocument, titleSpace } from "./story-document";
import type { Node } from "./story-markdown";
import { A4, COLOR, HEADER, LOGO, MEASURE, MM, NATURAL, PAGE, SPACE, STEP } from "./theme";

const input = { title: "Crima din seră", language: "ro", markdown: "Ștefan a venit." };

const uploaded = (width: number, height: number) => ({ src: "data:image/jpeg;base64,", width, height });

function content(definition: Node): Node[] {
  return definition.content as Node[];
}

type Header = (page: number, pages: number, size: { width: number; height: number }) => Node;

/** The running header as the library asks for it on one page. */
function header(definition: Node, page = 1, pages = 1): Node {
  return (definition.header as Header)(page, pages, A4);
}

/** The header's row: the logo's cell, if any, then the title's. */
function row(header: Node): Node[] {
  const [line] = header.stack as Node[];
  return line.columns as Node[];
}

function headerRule(header: Node): Node {
  const [, rule] = header.stack as Node[];
  return rule;
}

/** Every node of a tree of content, depth first. */
function nodes(node: unknown): Node[] {
  if (Array.isArray(node)) return node.flatMap(nodes);
  if (typeof node !== "object" || node === null) return [];
  const found = node as Node;
  return [found, ...["stack", "columns", "ul", "ol", "text"].flatMap((key) => (Array.isArray(found[key]) ? nodes(found[key]) : []))];
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

  test("is the app bar's mark set large: a 28 pt ink square with the mark at three fifths of it, the card in paper and the rules in ink", () => {
    const [mark] = row(header(storyDocument({ ...input, logo: "mark" }).definition));
    const [square, card, ...rules] = mark.canvas as Node[];
    const xs = (card.points as { x: number }[]).map((point) => point.x);

    expect(LOGO.height).toBe(28);
    expect(mark.width).toBe(28);
    expect(square).toMatchObject({ type: "rect", x: 0, y: 0, w: 28, h: 28, color: COLOR.ink });
    // The card is 14 of the mark's 16 units, centred in the square.
    expect(Math.max(...xs) - Math.min(...xs)).toBeCloseTo((14 / 16) * 16.8, 6);
    expect(Math.min(...xs) + Math.max(...xs)).toBeCloseTo(28, 6);
    expect(card).toMatchObject({ type: "polyline", closePath: true, color: COLOR.paper });
    expect(rules.map((rule) => rule.color)).toEqual([COLOR.ink, COLOR.ink]);
  });
});

describe("the running header", () => {
  test.each([
    [480, 140, 96, 28],
    [100, 200, 14, 28],
    [2000, 100, 180, 9],
  ])("fits an uploaded %i x %i logo into the 28 pt high, at most 180 pt wide box as %f x %f", (width, height, printedWidth, printedHeight) => {
    const [logo] = row(header(storyDocument({ ...input, logo: uploaded(width, height) }).definition));

    expect(logo.image).toBe("logo");
    expect(logo.width).toBeCloseTo(printedWidth, 6);
    expect(logo.height).toBeCloseTo(printedHeight, 6);
    // A logo lower than the box is centred in it, so the row and the rule under it stand where they always do.
    const [, top, , bottom] = logo.margin as number[];
    expect(top + printedHeight + bottom).toBeCloseTo(LOGO.height, 6);
    expect(top).toBeCloseTo(bottom, 6);
  });

  test.each([1, 3])("prints the logo and the title on page %i of 3", (page) => {
    const { definition } = storyDocument({ ...input, logo: uploaded(480, 140) });
    const [logo, title] = row(header(definition, page, 3));

    expect(definition.images).toEqual({ logo: "data:image/jpeg;base64," });
    expect(logo).toMatchObject({ image: "logo" });
    expect(title).toMatchObject({ text: "Crima din seră" });
  });

  test("builds each page's header afresh, since the library consumes what it lays out", () => {
    const { definition } = storyDocument({ ...input, logo: "mark" });

    expect(header(definition, 1, 2)).not.toBe(header(definition, 2, 2));
    expect(row(header(definition, 1, 2))[1]).not.toBe(row(header(definition, 2, 2))[1]);
  });

  test("sets the title right-aligned in text-small Onest, ink-2, its line centred on the logo's row", () => {
    const [, title] = row(header(storyDocument({ ...input, logo: "mark" }).definition));
    const [, top, , bottom] = title.margin as number[];

    expect(title).toMatchObject({ font: "Onest", fontSize: STEP.small.size, color: COLOR.ink2, alignment: "right", width: "*" });
    expect(title.lineHeight ?? 1).toBe(1);
    expect(top).toBeCloseTo(bottom, 6);
    expect(top + NATURAL.Onest * STEP.small.size + bottom).toBeCloseTo(LOGO.height, 6);
  });

  test("prints the title, cut to its space, when the caller has fitted it", () => {
    const [, title] = row(header(storyDocument({ ...input, logo: "mark", headerTitle: "Crima…" }).definition));

    expect(title.text).toBe("Crima…");
  });

  test("still prints the title when there is no logo, alone on the row", () => {
    const columns = row(header(storyDocument({ ...input, logo: null }).definition));

    expect(columns).toHaveLength(1);
    expect(columns[0]).toMatchObject({ text: "Crima din seră", alignment: "right", width: "*" });
  });

  test("gives the title the measure less the logo and the gap before it", () => {
    expect(titleSpace(null)).toBeCloseTo(MEASURE, 6);
    expect(titleSpace("mark")).toBeCloseTo(MEASURE - 28 - HEADER.titleGap, 6);
    expect(titleSpace(uploaded(480, 140))).toBeCloseTo(MEASURE - 96 - HEADER.titleGap, 6);
  });

  test("spans the measure and stands on a 1px rule in line, as the app bar on its border", () => {
    const top = header(storyDocument({ ...input, logo: "mark" }).definition);
    const [line] = headerRule(top).canvas as Node[];

    expect(top.margin).toEqual([PAGE.side, HEADER.top, PAGE.side, 0]);
    expect(line).toMatchObject({ type: "line", x1: 0, x2: MEASURE, lineWidth: SPACE.rule, lineColor: COLOR.line });
  });

  test("mirrors the footer: as far from the sheet's edge as the page number, and its rule as far above the text as the number below it", () => {
    const top = header(storyDocument({ ...input, logo: "mark" }).definition);
    const [, ruleGap] = headerRule(top).margin as number[];
    const headerBottom = HEADER.top + LOGO.height + ruleGap + SPACE.rule;
    const numberLine = NATURAL.Onest * STEP.small.size;

    expect(PAGE.top - headerBottom).toBeCloseTo(PAGE.footer, 6);
    expect(HEADER.top).toBeCloseTo(PAGE.bottom - PAGE.footer - numberLine, 6);
  });
});

describe("the printable edge", () => {
  test("the page number's line and the header's logo box each stand at least 8 mm from the sheet's edge, which printers do not reach", () => {
    const { definition } = storyDocument({ ...input, logo: "mark" });
    const footer = (definition.footer as (page: number, pages: number) => Node)(1, 1);
    const [, belowText] = footer.margin as number[];
    const numberLine = NATURAL.Onest * STEP.small.size;

    // The footer starts at the bottom margin's top edge, and its line ends numberLine below its own margin.
    expect(PAGE.bottom - belowText - numberLine).toBeGreaterThanOrEqual(8 * MM);
    expect(HEADER.top).toBeGreaterThanOrEqual(8 * MM);
  });
});

describe("storyDocument", () => {
  test("the body begins with the title in the story's # style, and no logo: the header carries it", () => {
    for (const logo of [uploaded(480, 140), "mark" as const, null]) {
      const body = content(storyDocument({ ...input, logo }).definition);

      expect(body[0]).toMatchObject({ font: "Onest", fontSize: STEP.h2.size, color: COLOR.ink });
      expect(nodes(body).filter((node) => node.image !== undefined || node.canvas !== undefined)).toEqual([]);
    }
  });

  test("declares the contest's title as the document's own, whole even when the header cuts it", () => {
    const { definition } = storyDocument({ ...input, logo: "mark", headerTitle: "Crima…" });

    expect((definition.info as { title: string }).title).toBe("Crima din seră");
  });

  test("is A4, its column the measure centred on the sheet, in the story's language", () => {
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

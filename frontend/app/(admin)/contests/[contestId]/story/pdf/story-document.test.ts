import { describe, expect, test } from "vitest";

import { CAT, markSvg } from "@/lib/brand/marks.mjs";
import { uploaded } from "@/lib/pdf/pdf-test-support";
import { A4, COLOR, HEADER, LOGO, MEASURE, MM, NATURAL, PAGE, SPACE, STEP } from "@/lib/pdf/theme";
import type { Node } from "@/lib/pdf/types";

import { storyDocument } from "./story-document";

const input = { title: "Crima din seră", language: "ro", markdown: "Ștefan a venit." };

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
  test("is the cat the app bar draws, at the logo's height, in the light theme's colours", () => {
    const [mark] = row(header(storyDocument({ ...input, logo: "mark" }).definition));

    expect(LOGO.height).toBe(28);
    // The cat's box is 90 by 116 units.
    expect(mark).toMatchObject({ height: 28, width: (28 * 90) / 116 });
    expect(mark.svg).toBe(markSvg(CAT, { ink: COLOR.logoInk, chalk: COLOR.logoChalk }));
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
    // The footer's position is worked out for the line's natural height, so no leading, tracking or weight is added.
    for (const key of ["lineHeight", "characterSpacing", "bold"]) expect(footer(2, 5)).not.toHaveProperty(key);
  });

  test("never leaves a heading as the last thing on a page, though the header and footer follow it there", () => {
    const { definition } = storyDocument({ ...input, logo: "mark" });
    const breakBefore = definition.pageBreakBefore as (node: Node, nodes: { getFollowingNodesOnPage(): Node[]; getNodesOnNextPage(): Node[] }) => boolean;
    // Every node of a page's header and footer, as the library lists them after the story's.
    const flat = (node: Node): Node[] => [node, ...[...((node.stack as Node[]) ?? []), ...((node.columns as Node[]) ?? [])].flatMap(flat)];
    const running = (page: number) => [
      ...flat((definition.header as (page: number) => Node)(page)),
      ...flat((definition.footer as (page: number, pages: number) => Node)(page, 2)),
    ];
    const paragraph = { text: "Ștefan a venit." };
    const nodes = (following: Node[], next: Node[]) => ({ getFollowingNodesOnPage: () => following, getNodesOnNextPage: () => next });

    expect(running(1).length).toBeGreaterThan(2);
    expect(breakBefore({ headlineLevel: 2 }, nodes(running(1), [paragraph, ...running(2)]))).toBe(true);
    expect(breakBefore({ headlineLevel: 2 }, nodes([paragraph, ...running(1)], running(2)))).toBe(false);
    expect(breakBefore({}, nodes(running(1), [paragraph, ...running(2)]))).toBe(false);
    // A heading followed on its page only by another heading moves, and the run moves with it.
    expect(breakBefore({ headlineLevel: 1 }, nodes([{ headlineLevel: 2 }, ...running(1)], [paragraph, ...running(2)]))).toBe(true);
    // A heading that ends the story heads nothing, and stays where it is.
    expect(breakBefore({ headlineLevel: 2 }, nodes(running(1), []))).toBe(false);
  });

  test("reports the story's tables for measuring", () => {
    expect(storyDocument({ ...input, markdown: "| a |\n|---|\n| 1 |", logo: null }).tables).toHaveLength(1);
  });
});

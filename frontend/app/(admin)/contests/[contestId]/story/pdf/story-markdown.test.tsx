import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { StoryText } from "@/components/product/story-text";

import { fitColumns, givenTags, storyBlocks, type Node } from "./story-markdown";
import { ASCENDER, COLOR, DATA_SIZE, MEASURE, NATURAL, PX, SPACE, STEP, inset } from "./theme";

/**
 * The mapping is tested on the document tree it hands the PDF library, without
 * making a PDF: what face, size and colour each construct is set in, what holds
 * what, and the distances between blocks.
 */

type Fragment = Record<string, unknown> & { text: string };

const blocks = (markdown: string, widths?: number[][]) => storyBlocks(markdown, widths).content;

/** Every node of the tree, depth first, including inline fragments. */
function nodes(tree: unknown): Node[] {
  const out: Node[] = [];
  const visit = (value: unknown) => {
    if (Array.isArray(value)) return value.forEach(visit);
    if (!value || typeof value !== "object") return;
    const node = value as Node;
    out.push(node);
    for (const key of ["stack", "columns", "text", "table", "body"]) {
      const child = node[key];
      if (child && typeof child === "object") visit(child);
    }
  };
  visit(tree);
  return out;
}

/** The text a node holds, fragments joined. */
function textOf(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(textOf).join("");
  if (value && typeof value === "object") {
    const node = value as Node;
    return textOf(node.text ?? node.stack ?? node.columns ?? (node.table as Node | undefined)?.body ?? "");
  }
  return "";
}

/** The first node whose own text, fragments joined, contains `text`. */
function holding(tree: unknown, text: string): Node {
  const found = nodes(tree).find((node) => "text" in node && textOf(node.text).includes(text));
  if (!found) throw new Error(`nothing holds ${JSON.stringify(text)}`);
  return found;
}

/** The inline fragment whose text is exactly `text`. */
function fragment(tree: unknown, text: string): Fragment {
  const found = nodes(tree).find((node) => node.text === text) as Fragment | undefined;
  if (!found) throw new Error(`no fragment ${JSON.stringify(text)}`);
  return found;
}

const top = (node: Node) => (node.margin as number[] | undefined)?.[1] ?? 0;

describe("the story's type", () => {
  test("body text is Literata at the narrative size, its line as tall as the screen's, in ink", () => {
    const paragraph = holding(blocks("Дверь была заперта."), "Дверь");

    expect(paragraph).toMatchObject({ font: "Literata", fontSize: STEP.narrative.size, color: COLOR.ink });
    // The library multiplies the face's own height; CSS multiplies the size.
    expect((paragraph.lineHeight as number) * NATURAL.Literata * STEP.narrative.size).toBeCloseTo(1.7 * STEP.narrative.size, 6);
  });

  test("the story's # is text-h2 in Onest 400, ## is text-h3 and ### is text-row, both in Onest 500, each with its tracking", () => {
    const tree = blocks("# Один\n\n## Doi\n\n### Три");

    expect(holding(tree, "Один")).toMatchObject({ font: "Onest", fontSize: STEP.h2.size, bold: false, headlineLevel: 1 });
    expect(holding(tree, "Doi")).toMatchObject({ font: "Onest", fontSize: STEP.h3.size, bold: true, headlineLevel: 2 });
    expect(holding(tree, "Три")).toMatchObject({ font: "Onest", fontSize: STEP.row.size, bold: true, headlineLevel: 3 });
    expect(holding(tree, "Один").characterSpacing).toBeCloseTo(-0.042 * STEP.h2.size, 6);
    expect(holding(tree, "Doi").characterSpacing).toBeCloseTo(-0.02 * STEP.h3.size, 6);
  });

  test("tracked text is set word by word, so each word starts where the screen's letter-spacing puts it", () => {
    // The library lays a word out with its spacing between letters (n - 1 times)
    // but draws it after every letter (n times); each word is given n / (n - 1)
    // of the spacing so that its laid-out width is the screen's.
    const words = holding(blocks("# Ночь в оранжерее"), "Ночь").text as Fragment[];
    const spacing = -0.042 * STEP.h2.size;

    expect(words.map((word) => word.text)).toEqual(["Ночь ", "в ", "оранжерее"]);
    expect(words[0].characterSpacing).toBeCloseTo((spacing * 5) / 4, 6);
    expect(words[1].characterSpacing).toBeCloseTo(spacing * 2, 6);
    expect(words[2].characterSpacing).toBeCloseTo((spacing * 9) / 8, 6);
  });

  test("a deeper heading is body text, as the screen leaves it", () => {
    const heading = holding(blocks("#### Patru"), "Patru");

    expect(heading).toMatchObject({ font: "Literata", fontSize: STEP.narrative.size });
    expect(heading.headlineLevel).toBeUndefined();
  });

  test("strong is the semibold face, emphasis italic, and struck text is struck through", () => {
    const tree = blocks("**tare** *cursiv* ***ambele*** ~~tăiat~~");

    expect(fragment(tree, "tare")).toMatchObject({ bold: true });
    expect(fragment(tree, "cursiv")).toMatchObject({ italics: true });
    expect(fragment(tree, "ambele")).toMatchObject({ bold: true, italics: true });
    expect(fragment(tree, "tăiat").decoration).toEqual(["lineThrough"]);
  });

  test("inline code is the mono face at text-data on sunk, padded, and does not make its line taller", () => {
    const tree = blocks("Время: `22:40`, записано.");
    const code = fragment(tree, "22:40");

    expect(code).toMatchObject({ font: "JetBrainsMono", fontSize: DATA_SIZE, background: COLOR.sunk });
    expect((code.lineHeight as number) * NATURAL.JetBrainsMono * DATA_SIZE).toBeCloseTo(1.7 * STEP.narrative.size, 6);
    const line = holding(tree, "22:40").text as Fragment[];
    const at = line.indexOf(code);
    // The padding is a no-break space on the same ground, so it cannot be cut off at a line's end.
    expect(line[at - 1]).toMatchObject({ text: "\u00a0", background: COLOR.sunk });
    expect(line[at + 1]).toMatchObject({ text: "\u00a0", background: COLOR.sunk });
  });

  test("a link is the accent, underlined, and a real link only when the address is absolute", () => {
    const tree = blocks("[registrul](https://example.org/r) [pagina](/contests) [rău](javascript:alert(1))");

    expect(fragment(tree, "registrul")).toMatchObject({ link: "https://example.org/r", color: COLOR.accent, decoration: ["underline"] });
    expect(fragment(tree, "pagina")).toMatchObject({ color: COLOR.accent, decoration: ["underline"] });
    expect(fragment(tree, "pagina").link).toBeUndefined();
    expect(fragment(tree, "rău").link).toBeUndefined();
  });

  test("a soft line break is a space, a hard one a new line", () => {
    const paragraph = holding(blocks("unu\ndoi  \ntrei"), "unu");

    expect(textOf(paragraph.text)).toBe("unu doi\ntrei");
  });

  test("an image is its alternative text, and raw HTML is text, as on the screen", () => {
    expect(textOf(blocks("Camera: ![camera de la intrare](/c.png)"))).toContain("camera de la intrare");
    expect(textOf(blocks("a <b>raw</b> tag"))).toContain("<b>raw</b>");
  });

  test("a blank story, or one the editor left only breaks in, is nothing", () => {
    expect(blocks("  \n")).toEqual([]);
    expect(blocks("<br />\n\n<br />")).toEqual([]);
  });
});

describe("the story's blocks", () => {
  test("a list item's nested list sits inside that item, not beside it", () => {
    const [list] = blocks("- unu\n  - doi\n  - trei\n- patru");
    const items = list.stack as Node[];

    expect(items).toHaveLength(2);
    const content = (items[0].columns as Node[])[1].stack as Node[];
    expect(textOf(content[0])).toBe("unu");
    const nested = content[1].stack as Node[];
    expect(nested.map(textOf).map((text) => text.trim())).toEqual(["doi", "trei"]);
    expect(textOf(items[1])).toBe("patru");
  });

  test("an unordered item hangs a disc in the list's indent; an ordered one its number, from the list's start", () => {
    const [bullets] = blocks("- unu");
    const [numbers] = blocks("3. trei\n4. patru");

    const [marker] = (bullets.stack as Node[])[0].columns as Node[];
    expect(marker.width).toBe(SPACE.listIndent);
    expect((marker.canvas as Node[])[0]).toMatchObject({ type: "ellipse", color: COLOR.ink });
    const numbered = (numbers.stack as Node[]).map((item) => (item.columns as Node[])[0]);
    expect(numbered.map((cell) => cell.text)).toEqual(["3.", "4."]);
    expect(numbered[0]).toMatchObject({ alignment: "right", noWrap: true, width: SPACE.listIndent });
    // A space's width before the text: Literata's space is 0.2 em.
    expect(numbered[0].margin).toEqual([0, 0, 0.2 * STEP.narrative.size, 0]);
  });

  test("an item of one block is kept on one page, so its marker cannot be parted from its text", () => {
    const [list] = blocks("- unu\n- doi\n\n  trei");
    const [short, long] = list.stack as Node[];

    expect(short.unbreakable).toBe(true);
    expect(long.unbreakable).toBeUndefined();
  });

  test("a task item draws its box, filled when it is done", () => {
    const [list] = blocks("- [x] gata\n- [ ] nu");
    const box = (item: Node) => ((item.columns as Node[])[1].stack as Node[])[0].columns as Node[];
    const [done, open] = (list.stack as Node[]).map(box);

    expect(textOf(done[1])).toBe("gata");
    expect((done[0].canvas as Node[]).map((shape) => shape.type)).toEqual(["rect", "polyline"]);
    expect((open[0].canvas as Node[]).map((shape) => shape.type)).toEqual(["rect"]);
  });

  test("a block quote has the screen's rule on its left in line-2 and its text in ink-2", () => {
    const [quote] = blocks("> Лжёт не тот, кто молчит.");
    const layout = quote.layout as Record<string, (i: number) => unknown>;

    expect(layout.vLineWidth(0)).toBe(SPACE.quoteRule);
    expect(layout.vLineWidth(1)).toBe(0);
    expect(layout.vLineColor(0)).toBe(COLOR.line2);
    expect(layout.paddingLeft(0)).toBe(SPACE.quoteIndent);
    expect(holding(quote, "Лжёт")).toMatchObject({ color: COLOR.ink2 });
  });

  test("a code block keeps its indentation and blank lines, in the mono face on sunk", () => {
    const [block] = blocks("```sql\nSELECT 1\n\tFROM t\n\n  WHERE x;\n```");
    const cell = (block.table as Node).body as Node[][];
    const text = cell[0][0];

    expect(cell[0][0].fillColor).toBe(COLOR.sunk);
    expect(text).toMatchObject({ font: "JetBrainsMono", fontSize: DATA_SIZE, preserveLeadingSpaces: true });
    expect(textOf(text.text)).toBe("SELECT 1\n    FROM t\n\n  WHERE x;");
  });

  test("a code line too long for the column is broken anywhere rather than run off the page", () => {
    const long = "x".repeat(200);
    const [block] = blocks(`\`\`\`\nshort line\n${long}\n\`\`\``);
    const lines = (((block.table as Node).body as Node[][])[0][0].text as Fragment[]);

    expect(lines[0].wordBreak).toBeUndefined();
    expect(lines[1].wordBreak).toBe("break-all");
  });

  test("a rule is a hairline in line across the column", () => {
    const [rule] = blocks("---");

    expect((rule.canvas as Node[])[0]).toMatchObject({ type: "line", x2: MEASURE, lineWidth: PX, lineColor: COLOR.line });
  });
});

describe("the story's tables", () => {
  const TABLE = "| № | Nume | Ora |\n|--:|:----:|-----|\n| 1 | Ștefan | 21:58 |\n| 2 | Ana | 22:03 |";

  test("the header row repeats on every page and no row is split", () => {
    const table = blocks(TABLE)[0].table as Node;

    expect(table).toMatchObject({ headerRows: 1, dontBreakRows: true, keepWithHeaderRows: 1 });
  });

  test("header cells are the mono label, uppercase in ink-3; body cells Onest at text-body", () => {
    const body = (blocks(TABLE, [[20, 200, MEASURE - 220]])[0].table as Node).body as Node[][];

    expect(body[0][1]).toMatchObject({ font: "JetBrainsMono", fontSize: STEP.label.size, color: COLOR.ink3, text: [expect.objectContaining({ text: "NUME" })] });
    expect(body[0][1].characterSpacing).toBeCloseTo(0.1 * STEP.label.size, 6);
    expect(body[1][1]).toMatchObject({ font: "Onest", fontSize: STEP.body.size, color: COLOR.ink });
  });

  test("cells keep their column's alignment", () => {
    const body = (blocks(TABLE)[0].table as Node).body as Node[][];

    expect(body.map((row) => row.map((cell) => cell.alignment))).toEqual([
      ["right", "center", "left"],
      ["right", "center", "left"],
      ["right", "center", "left"],
    ]);
  });

  test("the rule under the header is line-2 and the rules under rows are line", () => {
    const layout = blocks(TABLE)[0].layout as Record<string, (i: number) => unknown>;

    expect([0, 1, 2, 3].map((i) => layout.hLineWidth(i))).toEqual([0, PX, PX, PX]);
    expect(layout.hLineColor(1)).toBe(COLOR.line2);
    expect(layout.hLineColor(2)).toBe(COLOR.line);
    expect(layout.vLineWidth(0)).toBe(0);
  });

  test("columns are as wide as they are given, and every table is reported for measuring", () => {
    const { content, tables } = storyBlocks(`${TABLE}\n\n${TABLE}`, [[30, 100, 249.8], [40, 90, 249.8]]);

    expect(tables).toHaveLength(2);
    expect(tables[0].width).toBe(MEASURE);
    expect((content[1].table as Node).widths).toEqual([40, 90, 249.8]);
    expect((storyBlocks(TABLE).content[0].table as Node).widths).toEqual(["auto", "auto", "auto"]);
  });
});

describe("fitColumns, the screen's automatic table layout", () => {
  test("spare width is shared in proportion to each column's widest content", () => {
    expect(fitColumns([5, 10, 20], [10, 20, 70], 200)).toEqual([20, 40, 140]);
  });

  test("a table too narrow for its widest content gives each column its least plus a fair share", () => {
    expect(fitColumns([10, 10], [30, 70], 60)).toEqual([20, 40]);
  });

  test("below the least it can take, every column keeps its least", () => {
    expect(fitColumns([30, 40], [50, 80], 50)).toEqual([30, 40]);
  });

  test("columns with nothing to measure share the width equally, never as NaN", () => {
    expect(fitColumns([0, 0], [0, 0], 100)).toEqual([50, 50]);
  });
});

describe("the story's footnotes", () => {
  const STORY = "Сторож остался.[^1]\n\n[^1]: Страница вырвана.";

  test("the reference is a superscript at three quarters of the text, a link to its note", () => {
    const tree = blocks(STORY);
    const reference = fragment(tree, "1");

    expect(reference).toMatchObject({ sup: true, color: COLOR.accent, linkToDestination: "user-content-fn-1" });
    // The library draws a superscript at 0.58 of its size.
    expect((reference.fontSize as number) * 0.58).toBeCloseTo(0.75 * STEP.narrative.size, 6);
    expect(holding(tree, "Страница").id).toBe("user-content-fn-1");
  });

  test("the way back is the screen's arrow, linked to the paragraph that cites it", () => {
    const tree = blocks(STORY);

    expect(fragment(tree, "↩")).toMatchObject({ font: "JetBrainsMono", linkToDestination: "user-content-fnref-1", color: COLOR.accent });
    expect(holding(tree, "Сторож").id).toBe("user-content-fnref-1");
  });

  test("the screen reader's 'Footnotes' label is left out, as the screen hides it", () => {
    expect(textOf(blocks(STORY))).not.toMatch(/footnotes/i);
  });
});

describe("the rhythm between blocks is the screen's", () => {
  test("paragraphs are mt-4 apart", () => {
    const [, second] = blocks("Unu.\n\nDoi.");

    expect(top(second)).toBeCloseTo(SPACE.block, 6);
  });

  test("a heading keeps its own space above, with its text set where the screen's line box puts it", () => {
    const [, heading, after] = blocks("Unu.\n\n## Doi\n\nTrei.");

    expect(top(heading)).toBeCloseTo(SPACE.h3 + inset(STEP.h3) - inset(STEP.narrative), 6);
    expect(top(after)).toBeCloseTo(SPACE.block + inset(STEP.narrative) - inset(STEP.h3), 6);
  });

  test("a list's first item does not add its mt-1.5 to the list's mt-4: on the screen the two margins collapse", () => {
    const [, list] = blocks("Unu.\n\n- a\n- b");
    const [first, second] = list.stack as Node[];

    expect(top(list)).toBeCloseTo(SPACE.block, 6);
    expect(top(first)).toBe(0);
    expect(top(second)).toBeCloseTo(SPACE.item, 6);
  });

  test("the story begins with its first block's own space, for the page to place under the title", () => {
    expect(storyBlocks("# Titlu").mt).toBe(SPACE.h2);
    expect(storyBlocks("Unu.").mt).toBe(0);
    expect(storyBlocks("Unu.").first).toBeCloseTo(inset(STEP.narrative), 6);
  });

  test("a disc sits where the screen paints it, on the item's first line", () => {
    const [list] = blocks("- a");
    const disc = (((list.stack as Node[])[0].columns as Node[])[0].canvas as Node[])[0];
    const size = STEP.narrative.size;

    // Measured in Chromium: a 7 px disc at 18 px text, its centre 17.66 px before the text and 7.6 px above the baseline.
    expect(disc.r1).toBeCloseTo((3.5 / 18) * size, 3);
    expect(disc.x).toBeCloseTo(SPACE.listIndent - (17.66 / 18) * size, 3);
    expect(disc.y).toBeCloseTo(ASCENDER.Literata * size - (7.6 / 18) * size, 3);
  });
});

describe("parity with StoryText", () => {
  const KITCHEN_SINK = [
    "# H1",
    "## H2",
    "### H3",
    "#### H4",
    "Text with *em*, **strong**, ~~del~~, `code`, a [link](https://example.org), ![alt](x.png) and <b>raw</b>",
    "a  \nhard break[^n]",
    "- [x] task",
    "  - nested",
    "1. one",
    "> quote",
    "---",
    "```sql\nSELECT 1;\n```",
    "| a | b |\n|---|:-:|\n| 1 | 2 |",
    "[^n]: Note.",
  ].join("\n\n");

  test("it is given the very elements StoryText puts on the screen, in the same order, and maps every one", () => {
    // StoryText is the reference: if it starts producing an element the
    // mapping has never seen, this fails rather than the PDF.
    const { container } = render(<StoryText markdown={KITCHEN_SINK} />);
    const screen = [...container.querySelectorAll("*")].slice(1).map((element) => element.tagName.toLowerCase());

    expect(screen.length).toBeGreaterThan(30);
    expect(givenTags(KITCHEN_SINK)).toEqual(screen);
    expect(() => storyBlocks(KITCHEN_SINK)).not.toThrow();
  });
});

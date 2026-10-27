import { Children, Fragment, isValidElement, type ReactElement, type ReactNode } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

import { cleanEditorMarkdown, safeUrl } from "@/lib/format/markdown";

import { ASCENDER, COLOR, DATA_SIZE, MEASURE, NATURAL, PX, SPACE, STEP, inset, lineHeight, type Face, type Step } from "./theme";

/**
 * The story's Markdown as the PDF library's document tree, set as `StoryText`
 * sets it on the screen.
 *
 * It reads the same tree `StoryText` renders: `react-markdown` with GFM, the
 * same cleaning and the same address check, called without components so each
 * element arrives under its HTML tag. Every tag that tree can hold has a case
 * below; one without a case throws rather than print the story incomplete.
 *
 * Two differences between CSS and the library shape the code:
 * - CSS centres a line's text in its line box; the library puts the text at
 *   the top of the box and all the extra leading below. Each block therefore
 *   carries the distance (`first`, `last`) between the two, and the space
 *   above the next block is corrected by it, so text sits where the screen
 *   puts it.
 * - CSS collapses a box's top margin into its first child's; the library adds
 *   them. `stack` collapses them as CSS does.
 */

/** A node of the library's document tree. */
export type Node = Record<string, unknown>;

/** A table, with the width its column has, for the caller to measure its content. */
export type TableRef = { node: Node; width: number };

type Block = {
  node: Node;
  /** The CSS margin above it, in pt. */
  mt: number;
  /** How far below the library's place for its first and last line the screen sets them. */
  first: number;
  last: number;
};

type Context = {
  /** The width the block may take, in pt. */
  width: number;
  step: Step;
  color: string;
  /** Whether the block is a direct child of the story, the only blocks `mt-4` applies to. */
  root: boolean;
  tables: TableRef[];
  widths: number[][] | undefined;
};

type Marks = {
  bold?: boolean;
  italics?: boolean;
  strike?: boolean;
  /** Set on a link: its address when the PDF can follow it, `null` when it is only styled as one. */
  href?: string | null;
  /** An in-document destination, for a footnote's links. */
  destination?: string;
  sup?: boolean;
  /** Text that is set uppercase, as a table's header is. */
  upper?: boolean;
};

type Element = ReactElement<Record<string, unknown> & { children?: ReactNode }>;

const BLOCKS = new Set(["h1", "h2", "h3", "h4", "h5", "h6", "p", "blockquote", "pre", "hr", "ul", "ol", "table", "section"]);

/** The headings `StoryText` sets apart; deeper ones it leaves as body text. */
const HEADINGS: Record<string, { step: Step; mt: number; level: number }> = {
  h1: { step: STEP.h2, mt: SPACE.h2, level: 1 },
  h2: { step: STEP.h3, mt: SPACE.h3, level: 2 },
  h3: { step: STEP.row, mt: SPACE.row, level: 3 },
};

const pitch = (step: Step) => step.leading * step.size;

/** The style every text node of a step carries. */
function textStyle(step: Step, color: string): Node {
  return {
    font: step.face,
    fontSize: step.size,
    lineHeight: lineHeight(step),
    characterSpacing: step.tracking * step.size,
    bold: step.medium,
    color,
  };
}

function parse(markdown: string): ReactNode {
  const text = cleanEditorMarkdown(markdown);
  if (text.trim() === "") return null;
  const root = Markdown({ children: text, remarkPlugins: [remarkGfm], urlTransform: safeUrl }) as Element;
  return root.props.children;
}

function elementsOf(children: ReactNode): Element[] {
  return Children.toArray(children).filter(isValidElement) as Element[];
}

function plainText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(plainText).join("");
  if (isValidElement(node)) return plainText((node as Element).props.children);
  return "";
}

/** The tags the parser produced, in document order: what the mapping is given. */
export function givenTags(markdown: string): string[] {
  const out: string[] = [];
  const visit = (node: ReactNode) => {
    for (const element of elementsOf(node)) {
      if (element.type !== Fragment) out.push(String(element.type));
      visit(element.props.children);
    }
  };
  visit(parse(markdown));
  return out;
}

// Inline content.

/** White space as CSS's `white-space: normal` reads it; a no-break space is not white space there. */
const collapse = (text: string) => text.replace(/[ \t\n\r\f]+/g, " ");

function fragment(text: string, context: Context, marks: Marks): Node {
  const link = marks.href !== undefined || marks.destination !== undefined;
  const decoration = [...(link ? ["underline"] : []), ...(marks.strike ? ["lineThrough"] : [])];
  const size = marks.sup ? (0.75 / 0.58) * context.step.size : context.step.size;
  return {
    text: marks.upper ? text.toLocaleUpperCase() : text,
    ...(marks.bold ? { bold: true } : {}),
    ...(marks.italics ? { italics: true } : {}),
    ...(marks.sup ? { sup: true, fontSize: size } : {}),
    ...(link ? { color: COLOR.accent } : {}),
    ...(marks.href ? { link: marks.href } : {}),
    ...(marks.destination ? { linkToDestination: marks.destination } : {}),
    ...(decoration.length > 0 ? { decoration, decorationThickness: 0.05 * size } : {}),
  };
}

/** Inline code: the mono face at `text-data` on sunk, padded `px-1` with a no-break space on the same ground. */
function code(text: string, context: Context, marks: Marks): Node[] {
  const pad = { ...fragment("\u00a0", context, { ...marks, href: undefined, destination: undefined, strike: false }), background: COLOR.sunk };
  return [
    pad,
    {
      ...fragment(collapse(text), context, marks),
      font: "JetBrainsMono",
      fontSize: DATA_SIZE,
      // As tall as the line it sits in: on the screen it never makes its line taller.
      lineHeight: pitch(context.step) / (NATURAL.JetBrainsMono * DATA_SIZE),
      characterSpacing: 0,
      background: COLOR.sunk,
    },
    pad,
  ];
}

/**
 * Text with letter-spacing, a fragment per word. The library lays a word out
 * with its spacing between letters (n - 1 times) but draws it after every
 * letter (n times), so each following word would start one spacing late. A
 * word given n / (n - 1) of the spacing lays out as wide as on the screen;
 * the difference is spread inside the word, where it is a fraction of a point.
 */
export function tracked(node: Node, spacing: number): Node[] {
  if (spacing === 0 || typeof node.text !== "string") return [node];
  return node.text
    .split(/(?<=[ -])/)
    .map((word) => ({ ...node, text: word, characterSpacing: word.length > 1 ? (spacing * word.length) / (word.length - 1) : spacing }));
}

function inline(children: ReactNode, context: Context, marks: Marks = {}): Node[] {
  return Children.toArray(children).flatMap((child): Node[] => {
    if (typeof child === "string" || typeof child === "number") {
      const text = fragment(collapse(String(child)), context, marks);
      return tracked(text, context.step.tracking * ((text.fontSize as number | undefined) ?? context.step.size));
    }
    if (!isValidElement(child)) return [];
    const element = child as Element;
    const props = element.props;

    switch (element.type) {
      case "em":
        return inline(props.children, context, { ...marks, italics: true });
      case "strong":
        return inline(props.children, context, { ...marks, bold: true });
      case "del":
        return inline(props.children, context, { ...marks, strike: true });
      case "sup":
        return inline(props.children, context, { ...marks, sup: true });
      case "code":
        return code(plainText(props.children), context, marks);
      case "br":
        return [{ text: "\n" }];
      case "img":
        // A picture is its text alternative: the screen shows that text, beside a broken picture.
        return [fragment(String(props.alt ?? ""), context, marks)];
      case "input":
        // A task's box is drawn by its list item (`taskBox`).
        return [];
      case "a": {
        const href = String(props.href ?? "");
        const anchor = href.startsWith("#") ? href.slice(1) : undefined;
        if ("data-footnote-backref" in props) {
          // Literata has no ↩; the screen borrows a system face for it, paper the mono face.
          return [{ ...fragment("↩", context, { ...marks, destination: anchor }), font: "JetBrainsMono", characterSpacing: 0 }];
        }
        const absolute = /^(https?|mailto):/i.test(href);
        return inline(props.children, context, { ...marks, href: absolute ? href : null, destination: anchor });
      }
      default:
        throw new Error(`the story PDF has no mapping for <${String(element.type)}>`);
    }
  });
}

/** The ids of the footnote references a paragraph holds, which their notes link back to. */
function referenceIds(children: ReactNode): string[] {
  return elementsOf(children).flatMap((element) =>
    element.type === "a" && "data-footnote-ref" in element.props && typeof element.props.id === "string"
      ? [element.props.id]
      : referenceIds(element.props.children),
  );
}

// Blocks.

/** A block's text without the white space at its ends, which CSS does not set either. */
function trimmed(fragments: Node[]): Node[] {
  const ends = [
    [0, /^[ \t\n\r\f]+/],
    [fragments.length - 1, /[ \t\n\r\f]+$/],
  ] as const;
  const out = [...fragments];
  for (const [at, space] of ends) {
    if (out[at] && typeof out[at].text === "string" && out[at].text !== "\n") {
      out[at] = { ...out[at], text: (out[at].text as string).replace(space, "") };
    }
  }
  // A line a hard break starts does not begin with the space the source had there.
  return out
    .map((fragment, i) =>
      i > 0 && out[i - 1].text === "\n" && typeof fragment.text === "string"
        ? { ...fragment, text: fragment.text.replace(ends[0][1], "") }
        : fragment,
    )
    .filter((fragment) => fragment.text !== "");
}

function textBlock(children: ReactNode, context: Context, step: Step, extra: Node = {}): Block {
  const style = { ...context, step };
  const [id] = referenceIds(children);
  return {
    node: { text: trimmed(inline(children, style)), ...textStyle(step, context.color), ...(id ? { id } : {}), ...extra },
    mt: 0,
    first: inset(step),
    last: inset(step),
  };
}

/**
 * Children stacked as CSS stacks them. Bottom margins are always zero here
 * (the prose sets only `margin-top`), so the one collapse is a first child's
 * margin into a parent without top padding (`collapses`): the parent takes the
 * larger of the two and the child sits at its top.
 */
function stack(children: Block[], collapses: boolean): Omit<Block, "node"> & { nodes: Node[] } {
  const nodes = children.map((child, index) => {
    const previous = children[index - 1];
    const space =
      index === 0
        ? collapses
          ? 0
          : child.mt + child.first
        : child.mt + child.first - previous.last;
    return space === 0 ? child.node : { ...child.node, margin: [0, space, 0, 0] };
  });
  const first = children[0];
  const last = children[children.length - 1];
  return {
    nodes,
    mt: collapses && first ? first.mt : 0,
    first: collapses && first ? first.first : 0,
    last: last ? last.last : 0,
  };
}

/** Block children of a container. A tight list item holds bare text beside its blocks; that text is a paragraph. */
function blocks(children: ReactNode, context: Context): Block[] {
  const out: Block[] = [];
  let run: ReactNode[] = [];
  const close = () => {
    if (run.some((node) => isValidElement(node) || String(node).trim() !== "")) {
      out.push(textBlock(run, context, context.step));
    }
    run = [];
  };

  for (const child of Children.toArray(children)) {
    if (isValidElement(child) && BLOCKS.has(String(child.type))) {
      close();
      const converted = block(child as Element, context);
      if (converted) out.push(converted);
    } else {
      run.push(child);
    }
  }
  close();

  // `[&>*+*]:mt-4`: every child of the story but the first. A heading's own margin outranks it.
  if (context.root) {
    out.forEach((child, index) => {
      if (index > 0) child.mt = Math.max(child.mt, SPACE.block);
    });
  }
  return out;
}

function block(element: Element, context: Context): Block | null {
  const props = element.props;
  const tag = String(element.type);
  const nested = { ...context, root: false };

  // The footnotes' label, which the screen hides from sight (`sr-only`).
  if (tag === "h2" && String(props.className ?? "").includes("sr-only")) return null;

  const heading = HEADINGS[tag];
  if (heading) {
    const converted = textBlock(props.children, nested, heading.step, { headlineLevel: heading.level });
    return { ...converted, mt: heading.mt };
  }

  switch (tag) {
    case "h4":
    case "h5":
    case "h6":
    case "p":
      return textBlock(props.children, nested, context.step);
    case "blockquote":
      return quote(props.children, nested);
    case "pre":
      return codeBlock(plainText(props.children), nested);
    case "hr":
      return {
        node: { canvas: [{ type: "line", x1: 0, y1: PX / 2, x2: context.width, y2: PX / 2, lineWidth: PX, lineColor: COLOR.line }] },
        mt: 0,
        first: 0,
        last: 0,
      };
    case "ul":
    case "ol":
      return list(element, nested);
    case "section": {
      // The footnotes: their label, which the screen hides, and their list.
      const inner = stack(blocks(props.children, nested), true);
      return { node: { stack: inner.nodes }, ...inner };
    }
    case "table":
      return table(element, nested);
    default:
      throw new Error(`the story PDF has no mapping for <${tag}>`);
  }
}

function quote(children: ReactNode, context: Context): Block {
  const inner = stack(
    blocks(children, { ...context, color: COLOR.ink2, width: context.width - SPACE.quoteRule - SPACE.quoteIndent }),
    true,
  );
  return {
    node: {
      table: { widths: ["*"], body: [[{ stack: inner.nodes }]] },
      layout: {
        hLineWidth: () => 0,
        vLineWidth: (i: number) => (i === 0 ? SPACE.quoteRule : 0),
        vLineColor: () => COLOR.line2,
        paddingLeft: () => SPACE.quoteIndent,
        paddingRight: () => 0,
        paddingTop: () => 0,
        paddingBottom: () => 0,
      },
    },
    ...inner,
  };
}

/** Code blocks of more lines than this may break across a page; shorter ones never do, as the print rule asks. */
const UNBROKEN_CODE_LINES = 30;

function codeBlock(source: string, context: Context): Block {
  // Tabs reach the next stop of four, `tab-size` as the screen sets it.
  const lines = source
    .replace(/\n$/, "")
    .split("\n")
    .map((line) => line.replace(/\t/g, (_tab, offset: number) => " ".repeat(4 - (offset % 4))));

  // The screen's lines are as tall as the block's own line (the mono face at the narrative size), and the
  // smaller code sits on that line's baseline, lower than the library would put it.
  const strut = { ...context.step, face: "JetBrainsMono" as Face };
  const drop = inset(strut) + ASCENDER.JetBrainsMono * (context.step.size - DATA_SIZE);
  const width = context.width - 2 * SPACE.codePad;
  // Every glyph of the mono face is 0.6 em wide.
  const fits = Math.floor(width / (0.6 * DATA_SIZE));

  const text = lines.map((line, index) => ({
    text: index < lines.length - 1 ? `${line}\n` : line,
    ...(line.split(" ").some((word) => word.length > fits) ? { wordBreak: "break-all" } : {}),
  }));

  return {
    node: {
      table: {
        widths: ["*"],
        body: [
          [
            {
              text,
              font: "JetBrainsMono",
              fontSize: DATA_SIZE,
              lineHeight: pitch(context.step) / (NATURAL.JetBrainsMono * DATA_SIZE),
              color: context.color,
              preserveLeadingSpaces: true,
              fillColor: COLOR.sunk,
            },
          ],
        ],
      },
      layout: {
        hLineWidth: () => 0,
        vLineWidth: () => 0,
        paddingLeft: () => SPACE.codePad,
        paddingRight: () => SPACE.codePad,
        paddingTop: () => SPACE.codePad + drop,
        paddingBottom: () => SPACE.codePad - drop,
      },
      ...(lines.length <= UNBROKEN_CODE_LINES ? { unbreakable: true } : {}),
    },
    mt: 0,
    first: 0,
    last: 0,
  };
}

/**
 * The disc as Chromium paints it beside 18 px text, in em of the text: 7 px
 * across, its centre 17.66 px before the text and 7.6 px above the baseline.
 */
const DISC = { radius: 3.5 / 18, before: 17.66 / 18, above: 7.6 / 18 };

/** A space's width in each face, in em. */
const SPACE_WIDTH: Record<Face, number> = { Literata: 0.2, Onest: 0.27, JetBrainsMono: 0.6 };

function marker(ordered: boolean, number: number, context: Context): Node {
  const size = context.step.size;
  if (ordered) {
    // Right-aligned a space before the text and never wrapped, so a long number hangs into the margin, as an
    // outside marker does. The space is a margin: the library trims a trailing space, a no-break one too.
    return {
      width: SPACE.listIndent,
      text: `${number}.`,
      alignment: "right",
      noWrap: true,
      margin: [0, 0, SPACE_WIDTH[context.step.face] * size, 0],
      ...textStyle(context.step, context.color),
    };
  }
  return {
    width: SPACE.listIndent,
    canvas: [
      {
        type: "ellipse",
        x: SPACE.listIndent - DISC.before * size,
        y: ASCENDER[context.step.face] * size - DISC.above * size,
        r1: DISC.radius * size,
        r2: DISC.radius * size,
        color: context.color,
      },
    ],
  };
}

/** The screen's disabled checkbox: 13 px, its bottom on the baseline, in the nearest tokens to the browser's greys. */
function taskBox(checked: boolean, context: Context): Node {
  const side = 13 * PX;
  const y = ASCENDER[context.step.face] * context.step.size - side;
  const box = { type: "rect", x: 0, y, w: side, h: side, r: 2 * PX, lineWidth: PX };
  if (!checked) return { width: side, canvas: [{ ...box, color: COLOR.sunk, lineColor: COLOR.ann }] };
  const at = (x: number, down: number) => ({ x: x * PX, y: y + down * PX });
  return {
    width: side,
    canvas: [
      { ...box, color: COLOR.ann, lineColor: COLOR.ann },
      { type: "polyline", points: [at(3.2, 6.8), at(5.4, 9.2), at(9.9, 3.9)], lineWidth: 1.6 * PX, lineColor: COLOR.paper, lineCap: "round" },
    ],
  };
}

function listItem(item: Element, ordered: boolean, number: number, context: Context): Block {
  const width = context.width - SPACE.listIndent;
  const children = Children.toArray(item.props.children);
  const box = children.find((child) => isValidElement(child) && child.type === "input") as Element | undefined;

  let inner: ReturnType<typeof stack>;
  if (box) {
    // The box sits before the text, a space apart, as the browser lays out an inline control.
    const gap = SPACE_WIDTH[context.step.face] * context.step.size;
    const text = stack(blocks(children.filter((child) => child !== box), { ...context, width: width - 13 * PX - gap }), true);
    inner = { ...text, nodes: [{ columns: [taskBox(Boolean(box.props.checked), context), { width: "*", stack: text.nodes }], columnGap: gap }] };
  } else {
    inner = stack(blocks(children, { ...context, width }), true);
  }

  const id = typeof item.props.id === "string" ? item.props.id : undefined;
  const content = inner.nodes.map((node, index) => (index === 0 && id && "text" in node ? { ...node, id } : node));
  return {
    node: {
      columns: [marker(ordered, number, context), { width: "*", stack: content }],
      columnGap: 0,
      // The library breaks each column on its own, which can leave a marker on one page and its text on the
      // next; an item of one block is kept whole.
      ...(content.length === 1 ? { unbreakable: true } : {}),
    },
    mt: Math.max(SPACE.item, inner.mt),
    first: inner.first,
    last: inner.last,
  };
}

function list(element: Element, context: Context): Block {
  const ordered = element.type === "ol";
  const start = typeof element.props.start === "number" ? element.props.start : 1;
  const items = elementsOf(element.props.children).map((item, index) => listItem(item, ordered, start + index, context));
  const inner = stack(items, true);
  return { node: { stack: inner.nodes }, ...inner };
}

type Align = "left" | "right" | "center";

function alignOf(props: Record<string, unknown>): Align {
  const value = props.align ?? (props.style as { textAlign?: string } | undefined)?.textAlign;
  return value === "right" || value === "center" ? value : "left";
}

/**
 * A header cell is the mono label; a body cell is Onest at `text-body`. Both
 * get the body cells' `pr-4`: the screen's header cells have none, which runs
 * neighbouring headers together and sets a right-aligned header off its
 * column's figures.
 */
function cell(element: Element, header: boolean, context: Context): Node {
  const step = header ? STEP.label : STEP.body;
  const color = header ? COLOR.ink3 : context.color;
  const style = { ...context, step };
  const shift = inset(step);
  return {
    text: inline(element.props.children, style, header ? { upper: true } : {}),
    ...textStyle(step, color),
    alignment: alignOf(element.props),
    margin: [0, SPACE.cellY + shift, SPACE.cellRight, SPACE.cellY - shift],
  };
}

function table(element: Element, context: Context): Block {
  const rows = elementsOf(element.props.children).flatMap((part) =>
    elementsOf(part.props.children).map((row) => ({ row, header: part.type === "thead" })),
  );
  const body = rows.map(({ row, header }) => elementsOf(row.props.children).map((c) => cell(c, header, context)));
  const columns = body[0]?.length ?? 0;
  const index = context.tables.length;
  const given = context.widths?.[index];

  const node: Node = {
    table: {
      headerRows: 1,
      ...(body.length > 1 ? { keepWithHeaderRows: 1 } : {}),
      dontBreakRows: true,
      widths: given && given.length === columns ? given : Array.from({ length: columns }, () => "auto"),
      body,
    },
    layout: {
      hLineWidth: (i: number) => (i === 0 ? 0 : PX),
      hLineColor: (i: number) => (i === 1 ? COLOR.line2 : COLOR.line),
      vLineWidth: () => 0,
      paddingLeft: () => 0,
      paddingRight: () => 0,
      paddingTop: () => 0,
      paddingBottom: () => 0,
    },
  };
  context.tables.push({ node, width: context.width });
  return { node, mt: 0, first: 0, last: 0 };
}

/**
 * The widths the screen's automatic table layout gives columns that must fill
 * `width`, from each column's narrowest and widest content: spare width goes
 * in proportion to the widest content, and a shortfall is taken in proportion
 * to how much each column can give.
 */
export function fitColumns(least: number[], most: number[], width: number): number[] {
  const sum = (values: number[]) => values.reduce((total, value) => total + value, 0);
  const [min, max] = [sum(least), sum(most)];
  // Nothing measured, so nothing to share in proportion to.
  if (max <= 0) return least.map(() => width / least.length);
  if (max <= width) return most.map((value) => value + ((width - max) * value) / max);
  if (min >= width) return least;
  return least.map((value, i) => value + ((most[i] - value) * (width - min)) / (max - min));
}

/**
 * The story's blocks, each placed below the one before. `mt` and `first` are
 * the first block's own margin and line offset, for the page to place the
 * story below its title. `columnWidths`, one list per table in document
 * order, comes from measuring `tables`; without it columns are sized by the
 * library alone.
 */
export function storyBlocks(
  markdown: string,
  columnWidths?: number[][],
): { content: Node[]; mt: number; first: number; tables: TableRef[] } {
  const tables: TableRef[] = [];
  const context: Context = { width: MEASURE, step: STEP.narrative, color: COLOR.ink, root: true, tables, widths: columnWidths };
  const inner = stack(blocks(parse(markdown), context), true);
  return { content: inner.nodes, mt: inner.mt, first: inner.first, tables };
}

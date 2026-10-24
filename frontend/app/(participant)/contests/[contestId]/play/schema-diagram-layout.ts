import dagre from "@dagrejs/dagre";

import type { GameSchema } from "@/lib/api/schema";

/**
 * Where the schema diagram puts each table and each arrow, in diagram units
 * (CSS pixels at zoom 1). Pure, so the geometry is tested without a browser;
 * imported only by the diagram, so dagre stays in its lazy chunk.
 */

export const HEADER_HEIGHT = 30;
export const ROW_HEIGHT = 22;

/** `text-data` is 13px of JetBrains Mono, whose advance is 0.6em. */
const CHAR_WIDTH = 7.8;
export const PADDING = 10;
/** Room on a column's left for its PK/FK mark. */
export const MARK_WIDTH = 26;
const GAP = 16;
const MIN_WIDTH = 160;
const MAX_WIDTH = 480;
const MARGIN = 24;
const NODE_SEP = 32;
const RANK_SEP = 96;

export type DiagramColumn = {
  name: string;
  /** The name as drawn, cut with an ellipsis to fit the box. */
  label: string;
  /** The type as drawn, cut the same way, possibly to nothing. */
  typeLabel: string;
  type: string;
  primary: boolean;
  foreign: boolean;
};

/** One table's box; `x` and `y` are its top-left corner. */
export type DiagramNode = {
  name: string;
  label: string;
  x: number;
  y: number;
  width: number;
  height: number;
  columns: DiagramColumn[];
};

/**
 * One arrow, from a column's row to the row of the column it references.
 * `out1` and `out2` say which way the curve leaves each end: 1 to the right
 * of its box, -1 to the left.
 */
export type DiagramLink = { x1: number; y1: number; x2: number; y2: number; out1: 1 | -1; out2: 1 | -1 };

/**
 * One foreign key: an arrow per column pair, in the key's order. `id` joins
 * the table and key names with NUL, which no identifier contains; a dot can
 * be part of a quoted one.
 */
export type DiagramEdge = { id: string; name: string; from: string; to: string; links: DiagramLink[] };

export type Diagram = { nodes: DiagramNode[]; edges: DiagramEdge[]; width: number; height: number };

export function layoutDiagram(schema: GameSchema): Diagram {
  const nodes = schema.tables.map(sizeNode);
  const byName = new Map(nodes.map((node) => [node.name, node]));

  const connected = new Set<string>();
  const pairs = new Map<string, Pair>();
  for (const table of schema.tables) {
    for (const key of table.foreign_keys ?? []) {
      const to = key.referenced_table;
      if (to === table.name || !byName.has(to)) continue;
      connected.add(table.name).add(to);
      const id = `${table.name}\u0000${to}`;
      const pair = pairs.get(id) ?? { from: table.name, to, weight: 0 };
      pair.weight++;
      pairs.set(id, pair);
    }
  }

  const graphBottom = placeConnected(
    nodes.filter((node) => connected.has(node.name)),
    [...pairs.values()],
  );
  const graphWidth = Math.max(MARGIN, ...nodes.filter((n) => connected.has(n.name)).map((n) => n.x + n.width));
  placeLoose(
    nodes.filter((node) => !connected.has(node.name)),
    graphBottom,
    graphWidth,
  );

  const edges: DiagramEdge[] = [];
  for (const table of schema.tables) {
    const from = byName.get(table.name);
    if (!from) continue;
    for (const key of table.foreign_keys ?? []) {
      const to = byName.get(key.referenced_table);
      if (!to) continue;
      edges.push({
        id: `${table.name}\u0000${key.name}`,
        name: key.name,
        from: from.name,
        to: to.name,
        links: key.columns.map((column, i) => link(from, column, to, key.referenced_columns[i])),
      });
    }
  }

  const width = Math.max(0, ...nodes.map((n) => n.x + n.width)) + MARGIN;
  const height = Math.max(0, ...nodes.map((n) => n.y + n.height)) + MARGIN;
  return { nodes, edges, width: nodes.length ? width : 0, height: nodes.length ? height : 0 };
}

/** The SVG path of one arrow: a cubic curve leaving and entering sideways. */
export function linkPath({ x1, y1, x2, y2, out1, out2 }: DiagramLink): string {
  const reach = Math.max(32, Math.abs(x2 - x1) / 2);
  return `M ${x1} ${y1} C ${x1 + out1 * reach} ${y1}, ${x2 + out2 * reach} ${y2}, ${x2} ${y2}`;
}

function sizeNode(table: GameSchema["tables"][number]): DiagramNode {
  const primary = new Set(table.primary_key ?? []);
  const foreign = new Set((table.foreign_keys ?? []).flatMap((key) => key.columns));

  const longestName = Math.max(0, ...table.columns.map((c) => c.name.length));
  const longestType = Math.max(0, ...table.columns.map((c) => c.type.length));
  const wanted = Math.max(
    2 * PADDING + table.name.length * CHAR_WIDTH,
    2 * PADDING + MARK_WIDTH + (longestName + longestType) * CHAR_WIDTH + GAP,
  );
  const width = Math.ceil(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, wanted)));

  // The name keeps what it needs first: it is what a participant types. The
  // epsilon keeps a box sized to fit exactly from cutting its longest row.
  const room = Math.floor((width - 2 * PADDING - MARK_WIDTH - GAP) / CHAR_WIDTH + 1e-6);
  const columns = table.columns.map((column) => {
    const label = cut(column.name, room);
    return {
      name: column.name,
      label,
      typeLabel: cut(column.type, room - label.length),
      type: column.type,
      primary: primary.has(column.name),
      foreign: foreign.has(column.name),
    };
  });

  return {
    name: table.name,
    label: cut(table.name, Math.floor((width - 2 * PADDING) / CHAR_WIDTH)),
    x: 0,
    y: 0,
    width,
    height: HEADER_HEIGHT + table.columns.length * ROW_HEIGHT,
    columns,
  };
}

function cut(text: string, room: number): string {
  if (text.length <= room) return text;
  if (room <= 1) return "";
  return `${text.slice(0, room - 1)}…`;
}

type Pair = { from: string; to: string; weight: number };

/**
 * Lays out the tables a key joins, left to right, and returns the graph's
 * bottom edge. Dagre's cost follows the segments its edges are cut into, one
 * per rank an edge crosses, far more than the tables. The segments are
 * estimated first from our own longest-path ranks, which overcount dagre's.
 * Measured on 200 tables: up to 2500 estimated segments dagre's best takes
 * about 0.25 s; up to 5000 its cheaper ranker without the ordering heuristic
 * takes about 0.55 s, where the best takes twice that; at 50000 even the
 * cheaper one takes 9 s. Past the budget our ranks are used directly with a
 * single ordering pass, in milliseconds.
 */
function placeConnected(nodes: DiagramNode[], pairs: Pair[]): number {
  if (nodes.length === 0) return 0;

  const ranks = longestPathRanks(nodes, pairs);
  const segments = pairs.reduce((sum, pair) => sum + Math.abs(ranks.get(pair.to)! - ranks.get(pair.from)!), 0);
  if (segments > MAX_SEGMENTS) {
    flowColumns(orderWithinRanks(nodes, pairs, ranks), true);
  } else {
    placeWithDagre(nodes, pairs, segments > CAREFUL_SEGMENTS);
    const columns = new Map<number, DiagramNode[]>();
    for (const node of nodes) {
      const centre = Math.round(node.x + node.width / 2);
      columns.set(centre, [...(columns.get(centre) ?? []), node]);
    }
    const ordered = [...columns].sort(([a], [b]) => a - b).map(([, rank]) => rank.sort((a, b) => a.y - b.y));
    flowColumns(ordered, false);
  }
  return Math.max(...nodes.map((node) => node.y + node.height));
}

const CAREFUL_SEGMENTS = 2500;
const MAX_SEGMENTS = 5000;

/** Dagre, with its cheaper ranker and no ordering heuristic when `cheap`. */
function placeWithDagre(nodes: DiagramNode[], pairs: Pair[], cheap: boolean) {
  const options = {
    rankdir: "LR" as const,
    nodesep: NODE_SEP,
    ranksep: RANK_SEP,
    marginx: MARGIN,
    marginy: MARGIN,
    ...(cheap ? { ranker: "tight-tree" as const } : {}),
  };
  const graph = new dagre.graphlib.Graph();
  graph.setGraph(options);
  graph.setDefaultEdgeLabel(() => ({}));
  for (const node of nodes) graph.setNode(node.name, { width: node.width, height: node.height });
  for (const pair of pairs) graph.setEdge(pair.from, pair.to, { weight: pair.weight });
  dagre.layout(graph, { ...options, disableOptimalOrderHeuristic: cheap });

  for (const node of nodes) {
    const placed = graph.node(node.name);
    node.x = Math.round((placed.x ?? 0) - node.width / 2);
    node.y = Math.round((placed.y ?? 0) - node.height / 2);
  }
}

/**
 * Each table's rank: one past the furthest table pointing at it, so every
 * arrow runs left to right. A key that closes a cycle is ignored for ranking.
 */
function longestPathRanks(nodes: DiagramNode[], pairs: Pair[]): Map<string, number> {
  const out = new Map<string, string[]>(nodes.map((node) => [node.name, []]));
  for (const pair of pairs) out.get(pair.from)!.push(pair.to);

  // Iterative depth-first search: post-order reversed is a topological order
  // of the graph without its back edges.
  const state = new Map<string, "open" | "done">();
  const order: string[] = [];
  const forward = new Map<string, string[]>(nodes.map((node) => [node.name, []]));
  for (const root of nodes) {
    if (state.has(root.name)) continue;
    const stack: [string, number][] = [[root.name, 0]];
    state.set(root.name, "open");
    while (stack.length > 0) {
      const top = stack[stack.length - 1];
      const next = out.get(top[0])![top[1]++];
      if (next === undefined) {
        state.set(top[0], "done");
        order.push(top[0]);
        stack.pop();
      } else if (state.get(next) !== "open") {
        forward.get(top[0])!.push(next);
        if (!state.has(next)) {
          state.set(next, "open");
          stack.push([next, 0]);
        }
      }
    }
  }

  const ranks = new Map<string, number>(nodes.map((node) => [node.name, 0]));
  for (const name of order.reverse()) {
    for (const next of forward.get(name)!) ranks.set(next, Math.max(ranks.get(next)!, ranks.get(name)! + 1));
  }
  return ranks;
}

/** Groups tables by rank and orders each rank by where its referrers sit. */
function orderWithinRanks(nodes: DiagramNode[], pairs: Pair[], ranks: Map<string, number>): DiagramNode[][] {
  const columns: DiagramNode[][] = [];
  for (const node of nodes) (columns[ranks.get(node.name)!] ??= []).push(node);

  const referrers = new Map<string, string[]>(nodes.map((node) => [node.name, []]));
  for (const pair of pairs) referrers.get(pair.to)!.push(pair.from);

  const position = new Map<string, number>();
  return columns
    .filter((column) => column !== undefined)
    .map((column) => {
      const centre = (node: DiagramNode) => {
        const placed = referrers.get(node.name)!.flatMap((name) => position.get(name) ?? []);
        return placed.length ? placed.reduce((a, b) => a + b, 0) / placed.length : Infinity;
      };
      const sorted = column
        .map((node) => ({ node, centre: centre(node) }))
        .sort((a, b) => a.centre - b.centre)
        .map(({ node }) => node);
      sorted.forEach((node, i) => position.set(node.name, i / sorted.length));
      return sorted;
    });
}

/**
 * Left to right, a rank is a column, and a rank of many tables (a star of
 * tables all pointing at one) is a column tens of thousands of units tall
 * that fits the window only as specks. When `always`, or when a rank is
 * taller than a column should be, the ranks are re-flowed in order into
 * side-by-side columns of that height.
 */
function flowColumns(ranks: DiagramNode[][], always: boolean) {
  const nodes = ranks.flat();
  const area = nodes.reduce((sum, node) => sum + (node.width + NODE_SEP) * (node.height + NODE_SEP), 0);
  const limit = Math.max(1600, Math.sqrt(area) * 1.25);
  const stack = (rank: DiagramNode[]) => rank.reduce((sum, node) => sum + node.height + NODE_SEP, 0);
  if (!always && !ranks.some((rank) => stack(rank) > limit)) return;

  let x = MARGIN;
  for (const rank of ranks) {
    let y = MARGIN;
    let column = 0;
    for (const node of rank) {
      if (y > MARGIN && y + node.height > limit) {
        x += column + NODE_SEP;
        y = MARGIN;
        column = 0;
      }
      node.x = x;
      node.y = y;
      y += node.height + NODE_SEP;
      column = Math.max(column, node.width);
    }
    x += column + RANK_SEP;
  }
}

/**
 * Tables no key joins go on shelves below the graph, as wide as the graph or
 * as a landscape rectangle of their combined area, whichever is wider, and
 * returns the shelves' bottom edge. Stacked in dagre's first rank they would
 * make one column as tall as every table at once.
 */
function placeLoose(nodes: DiagramNode[], top: number, graphWidth: number): number {
  const area = nodes.reduce((sum, node) => sum + (node.width + NODE_SEP) * (node.height + NODE_SEP), 0);
  const right = Math.max(graphWidth, 1600, Math.sqrt(area) * 1.6);
  let x = MARGIN;
  let y = top + (top > 0 ? RANK_SEP / 2 : MARGIN);
  let shelf = 0;
  for (const node of nodes) {
    if (x > MARGIN && x + node.width > right) {
      x = MARGIN;
      y += shelf + NODE_SEP;
      shelf = 0;
    }
    node.x = x;
    node.y = y;
    x += node.width + NODE_SEP;
    shelf = Math.max(shelf, node.height);
  }
  return y + shelf;
}

/** An arrow's ends, on the facing sides of the two boxes. */
function link(from: DiagramNode, column: string, to: DiagramNode, referenced: string | undefined): DiagramLink {
  const y1 = rowMiddle(from, column);
  const y2 = rowMiddle(to, referenced);

  if (from !== to && to.x >= from.x + from.width) {
    return { x1: from.x + from.width, y1, x2: to.x, y2, out1: 1, out2: -1 };
  }
  if (from !== to && to.x + to.width <= from.x) {
    return { x1: from.x, y1, x2: to.x + to.width, y2, out1: -1, out2: 1 };
  }
  // The same box, or two in one rank: a loop out of the right side and back.
  return { x1: from.x + from.width, y1, x2: to.x + to.width, y2, out1: 1, out2: 1 };
}

/** A column's row middle, or the header's when the box does not carry it. */
function rowMiddle(node: DiagramNode, column: string | undefined): number {
  const index = column === undefined ? -1 : node.columns.findIndex((c) => c.name === column);
  if (index < 0) return node.y + HEADER_HEIGHT / 2;
  return node.y + HEADER_HEIGHT + index * ROW_HEIGHT + ROW_HEIGHT / 2;
}

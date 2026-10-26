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
 * A level run an arrow takes at `y`, from `enter` to `leave`, in the
 * direction it travels: past a column of other tables, or along its own
 * table's column until it is clear of the wider tables there.
 */
export type DiagramPassage = { enter: number; leave: number; y: number };

/**
 * One arrow, from a column's row to the row of the column it references.
 * `out1` and `out2` say which way the curve leaves each end: 1 to the right
 * of its box, -1 to the left. `via` lists its level runs, empty when a single
 * curve meets no other table.
 */
export type DiagramLink = {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  out1: 1 | -1;
  out2: 1 | -1;
  via: DiagramPassage[];
};

/**
 * One foreign key: an arrow per column pair, in the key's order. `id` joins
 * the table and key names with NUL, which no identifier contains; a dot can
 * be part of a quoted one. `referenced` is undefined where a truncated
 * document names fewer referenced columns than the key has.
 */
export type DiagramEdge = {
  id: string;
  name: string;
  from: string;
  to: string;
  columns: { column: string; referenced: string | undefined }[];
  links: DiagramLink[];
};

export type Diagram = { nodes: DiagramNode[]; edges: DiagramEdge[]; width: number; height: number };

export function layoutDiagram(schema: GameSchema): Diagram {
  const nodes = schema.tables.map(sizeNode);
  const byName = new Map(nodes.map((node) => [node.name, node]));

  const edges: DiagramEdge[] = [];
  for (const table of schema.tables) {
    for (const key of table.foreign_keys ?? []) {
      if (!byName.has(key.referenced_table)) continue;
      edges.push({
        id: `${table.name}\u0000${key.name}`,
        name: key.name,
        from: table.name,
        to: key.referenced_table,
        columns: key.columns.map((column, i) => ({ column, referenced: key.referenced_columns[i] })),
        links: [],
      });
    }
  }

  const connected = new Set<string>();
  const pairs = new Map<string, Pair>();
  for (const edge of edges) {
    if (edge.from === edge.to) continue;
    connected.add(edge.from).add(edge.to);
    const id = `${edge.from}\u0000${edge.to}`;
    const pair = pairs.get(id) ?? { from: edge.from, to: edge.to, weight: 0 };
    pair.weight++;
    pairs.set(id, pair);
  }

  const graph = nodes.filter((node) => connected.has(node.name));
  placeConnected(graph, [...pairs.values()], wiresOf(edges, byName));
  placeLoose(
    nodes.filter((node) => !connected.has(node.name)),
    graph,
  );

  const { width, height } = extent(nodes);
  return { nodes, edges: routeEdges(edges, nodes), width, height };
}

/** The arrows of `edges` between `nodes` where they now stand, which may be away from where the layout put them. */
export function routeEdges(edges: DiagramEdge[], nodes: DiagramNode[]): DiagramEdge[] {
  const byName = new Map(nodes.map((node) => [node.name, node]));
  return edges.map((edge) => routeEdge(edge, byName, nodes));
}

/** One edge's arrows; `byName` indexes `nodes`, which must carry both of its tables. */
export function routeEdge(edge: DiagramEdge, byName: Map<string, DiagramNode>, nodes: DiagramNode[]): DiagramEdge {
  const from = byName.get(edge.from)!;
  const to = byName.get(edge.to)!;
  return {
    ...edge,
    links: edge.columns.map(({ column, referenced }) => link(from, column, to, referenced, nodes)),
  };
}

/**
 * The rectangle the tables take with the drawing's margin around them. The
 * layout's own starts at the origin; tables moved by hand can lie left of or
 * above it.
 */
export function bounds(nodes: DiagramNode[]): { left: number; top: number; right: number; bottom: number } {
  if (nodes.length === 0) return { left: 0, top: 0, right: 0, bottom: 0 };
  return {
    left: Math.min(...nodes.map((n) => n.x)) - MARGIN,
    top: Math.min(...nodes.map((n) => n.y)) - MARGIN,
    right: Math.max(...nodes.map((n) => n.x + n.width)) + MARGIN,
    bottom: Math.max(...nodes.map((n) => n.y + n.height)) + MARGIN,
  };
}

/** The drawing's size with its margin, from the origin. */
function extent(nodes: DiagramNode[]): { width: number; height: number } {
  if (nodes.length === 0) return { width: 0, height: 0 };
  return {
    width: Math.max(...nodes.map((n) => n.x + n.width)) + MARGIN,
    height: Math.max(...nodes.map((n) => n.y + n.height)) + MARGIN,
  };
}

/**
 * The SVG path of one arrow: a cubic curve leaving and entering sideways, or,
 * with level runs, those runs joined by curves that bend only in the free
 * space between columns.
 */
export function linkPath({ x1, y1, x2, y2, out1, out2, via }: DiagramLink): string {
  if (via.length === 0) {
    const reach = Math.max(32, Math.abs(x2 - x1) / 2);
    return `M ${x1} ${y1} C ${x1 + out1 * reach} ${y1}, ${x2 + out2 * reach} ${y2}, ${x2} ${y2}`;
  }
  // Both control points at the middle: the curve keeps to the x-range between
  // its ends, and y between theirs.
  const bend = (ax: number, ay: number, bx: number, by: number) => {
    if (ax === bx && ay === by) return "";
    const mid = (ax + bx) / 2;
    return ` C ${mid} ${ay}, ${mid} ${by}, ${bx} ${by}`;
  };
  let path = `M ${x1} ${y1}`;
  let [x, y] = [x1, y1];
  for (const passage of via) {
    path += bend(x, y, passage.enter, passage.y);
    path += ` C ${passage.enter} ${passage.y}, ${passage.leave} ${passage.y}, ${passage.leave} ${passage.y}`;
    [x, y] = [passage.leave, passage.y];
  }
  return path + bend(x, y, x2, y2);
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
 * Lays out the tables a key joins, left to right. Dagre's cost follows the
 * segments its edges are cut into, one per rank an edge crosses, far more
 * than the tables. The segments are estimated first from our own
 * longest-path ranks, which overcount dagre's. Measured on 200 tables: up to
 * 2500 estimated segments dagre's best takes about 0.25 s; up to 5000 its
 * cheaper ranker without the ordering heuristic takes about 0.55 s, where the
 * best takes twice that; at 50000 even the cheaper one takes 9 s. Past the
 * budget our ranks are used directly with a single ordering pass, in
 * milliseconds.
 */
function placeConnected(nodes: DiagramNode[], pairs: Pair[], wires: Wire[]) {
  if (nodes.length === 0) return;

  const ranking = balanceLeaves(nodes, pairs);
  const ranks = longestPathRanks(nodes, ranking);
  const segments = ranking.reduce((sum, pair) => sum + Math.abs(ranks.get(pair.to)! - ranks.get(pair.from)!), 0);
  if (segments > MAX_SEGMENTS) {
    flowColumns(orderWithinRanks(nodes, ranking, ranks), true);
    return;
  }
  placeWithDagre(nodes, ranking, segments > CAREFUL_SEGMENTS);
  // Dagre centres a rank's boxes on one line, half a unit apart at most once
  // rounded, and keeps ranks apart, so a rank is a column of overlapping boxes.
  const ordered = columnsOf(nodes).map((column) => column.nodes.sort((a, b) => a.y - b.y));
  if (nodes.length <= REFINED_TABLES && wires.length <= REFINED_WIRES) refineOrder(ordered, wires);
  flowColumns(ordered, false);
  foldIntoBands(nodes);
  moveToMargin(nodes);
}

/**
 * `refineOrder` counts crossings pair by pair, many times over. Measured: 30
 * tables with 87 arrows take about 30 ms more; 60 with 236 took 0.8 s.
 */
const REFINED_TABLES = 40;
const REFINED_WIRES = 100;

/**
 * The keys as dagre should rank them. A table with many more keys on one side
 * than the other (a fact table and its dimensions, or a table every other
 * one references) would stand beside a column of all its neighbours; turning
 * round the keys of enough leaves, tables joined to nothing else, puts them
 * on its other side instead. A leaf has one key, so no cycle can appear.
 */
function balanceLeaves(nodes: DiagramNode[], pairs: Pair[]): Pair[] {
  const neighbours = new Map<string, Set<string>>(nodes.map((node) => [node.name, new Set()]));
  for (const pair of pairs) {
    neighbours.get(pair.from)!.add(pair.to);
    neighbours.get(pair.to)!.add(pair.from);
  }
  const isLeaf = (name: string) => neighbours.get(name)!.size === 1;

  const turned = new Set<Pair>();
  for (const node of nodes) {
    const outgoing = pairs.filter((pair) => pair.from === node.name);
    const incoming = pairs.filter((pair) => pair.to === node.name);
    const surplus = Math.floor(Math.abs(outgoing.length - incoming.length) / 2);
    const leaves =
      outgoing.length > incoming.length
        ? outgoing.filter((pair) => isLeaf(pair.to))
        : incoming.filter((pair) => isLeaf(pair.from));
    for (const pair of leaves.slice(0, surplus)) turned.add(pair);
  }
  return pairs.map((pair) => (turned.has(pair) ? { from: pair.to, to: pair.from, weight: pair.weight } : pair));
}

/** One arrow as the ordering sees it: its tables and its rows' heights within them. */
type Wire = { from: DiagramNode; fromY: number; to: DiagramNode; toY: number };

function wiresOf(edges: DiagramEdge[], byName: Map<string, DiagramNode>): Wire[] {
  return edges.flatMap((edge) => {
    const from = byName.get(edge.from)!;
    const to = byName.get(edge.to)!;
    if (from === to) return [];
    return edge.columns.map(({ column, referenced }) => ({
      from,
      fromY: rowMiddle(from, column) - from.y,
      to,
      toY: rowMiddle(to, referenced) - to.y,
    }));
  });
}

/**
 * Dagre orders the tables in a rank by their keys, but an arrow leaves from
 * its column's row, which dagre does not know: a fact table's dimensions in
 * dagre's order can have every arrow crossing the next. Sweeps across the
 * ranks reorder each by where its arrows' other ends sit, then swap
 * neighbours where that crosses fewer arrows; the best arrangement any sweep
 * reached is kept, and dagre's own when none crosses fewer.
 */
function refineOrder(ranks: DiagramNode[][], wires: Wire[]) {
  const rankOf = new Map<DiagramNode, number>();
  ranks.forEach((rank, i) => rank.forEach((node) => rankOf.set(node, i)));
  const between = wires.filter((wire) => rankOf.get(wire.from) !== rankOf.get(wire.to));
  const snapshot = () => ({ orders: ranks.map((rank) => [...rank]), ys: ranks.map((rank) => rank.map((n) => n.y)) });
  let count = crossingCount(between);
  let best = { count, ...snapshot() };

  for (let sweep = 0; sweep < 6 && best.count > 0; sweep++) {
    const rightward = sweep % 2 === 0;
    for (const rank of rightward ? ranks : [...ranks].reverse()) {
      if (rank.length < 2) continue;
      // From the side already swept only: averaging both sides holds a table
      // halfway between neighbours that both need to move.
      const index = rankOf.get(rank[0])!;
      const swept = between.filter((wire) => {
        const other = rankOf.get(wire.from) === index ? wire.to : wire.from;
        return rightward ? rankOf.get(other)! < index : rankOf.get(other)! > index;
      });
      const wanted = new Map(rank.map((node) => [node, wantedCentre(node, swept)]));
      const order = [...rank].sort((a, b) => wanted.get(a)! - wanted.get(b)!);
      if (order.some((node, i) => node !== rank[i])) {
        restack(rank, order);
        rank.splice(0, rank.length, ...order);
        count = crossingCount(between);
      }
      for (let i = 0; i + 1 < rank.length; i++) {
        const swapped = [...rank];
        [swapped[i], swapped[i + 1]] = [swapped[i + 1], swapped[i]];
        const before = rank.map((node) => node.y);
        restack(rank, swapped);
        const after = crossingCount(between);
        if (after < count) {
          count = after;
          rank.splice(0, rank.length, ...swapped);
        } else {
          rank.forEach((node, k) => (node.y = before[k]));
        }
      }
    }
    if (count < best.count) best = { count, ...snapshot() };
  }

  ranks.forEach((rank, i) => {
    rank.splice(0, rank.length, ...best.orders[i]);
    rank.forEach((node, k) => (node.y = best.ys[i][k]));
  });
}

/** Where a table's centre would put its rows level with the rows its arrows join. */
function wantedCentre(node: DiagramNode, wires: Wire[]): number {
  const wanted = wires.flatMap((wire) =>
    wire.from === node
      ? [wire.to.y + wire.toY - wire.fromY]
      : wire.to === node
        ? [wire.from.y + wire.fromY - wire.toY]
        : [],
  );
  const top = wanted.length ? wanted.reduce((a, b) => a + b, 0) / wanted.length : node.y;
  return top + node.height / 2;
}

/** Stacks `order` where `rank` stood, centred on the same height. */
function restack(rank: DiagramNode[], order: DiagramNode[]) {
  const top = Math.min(...rank.map((node) => node.y));
  const bottom = Math.max(...rank.map((node) => node.y + node.height));
  const total = order.reduce((sum, node) => sum + node.height, 0) + (order.length - 1) * NODE_SEP;
  let y = Math.round((top + bottom - total) / 2);
  for (const node of order) {
    node.y = y;
    y += node.height + NODE_SEP;
  }
}

/** Pairs of wires whose straight lines, side to side, cross. */
function crossingCount(wires: Wire[]): number {
  const lines = wires.map(({ from, fromY, to, toY }) => {
    const rightward = to.x >= from.x;
    return {
      ax: rightward ? from.x + from.width : from.x,
      ay: from.y + fromY,
      bx: rightward ? to.x : to.x + to.width,
      by: to.y + toY,
    };
  });
  const side = (px: number, py: number, qx: number, qy: number, rx: number, ry: number) =>
    Math.sign((qx - px) * (ry - py) - (qy - py) * (rx - px));
  let count = 0;
  for (let i = 0; i < lines.length; i++) {
    const a = lines[i];
    for (let j = i + 1; j < lines.length; j++) {
      const b = lines[j];
      if (
        side(a.ax, a.ay, a.bx, a.by, b.ax, b.ay) * side(a.ax, a.ay, a.bx, a.by, b.bx, b.by) < 0 &&
        side(b.ax, b.ay, b.bx, b.by, a.ax, a.ay) * side(b.ax, b.ay, b.bx, b.by, a.bx, a.by) < 0
      ) {
        count++;
      }
    }
  }
  return count;
}

/**
 * The diagram dialog's canvas on a large screen. A layout is shaped to fit
 * it: a drawing far wider or taller is shown smaller than it needs to be.
 */
const FRAME = { width: 1380, height: 820 };

function fitScale(width: number, height: number): number {
  return Math.min(1, FRAME.width / width, FRAME.height / height);
}

type Column = { x0: number; x1: number; nodes: DiagramNode[] };

/** The tables grouped into columns of boxes that overlap in x, left to right. */
function columnsOf(nodes: DiagramNode[]): Column[] {
  const columns: Column[] = [];
  for (const node of [...nodes].sort((a, b) => a.x - b.x)) {
    const last = columns.at(-1);
    if (last && node.x < last.x1) {
      last.x1 = Math.max(last.x1, node.x + node.width);
      last.nodes.push(node);
    } else {
      columns.push({ x0: node.x, x1: node.x + node.width, nodes: [node] });
    }
  }
  return columns;
}

/**
 * A long chain is one row of ranks, shown at a fraction of its size. Folded
 * into bands one under the other, every other band reversed so each rank
 * stays beside the next, it fits the frame larger; the fold is kept only
 * when it does, by a tenth at least.
 */
function foldIntoBands(nodes: DiagramNode[]) {
  const columns = columnsOf(nodes);
  if (columns.length < 3) return;
  const span = columns.at(-1)!.x1 - columns[0].x0;
  const heightOf = (band: Column[]) => {
    const boxes = band.flatMap((column) => column.nodes);
    return Math.max(...boxes.map((n) => n.y + n.height)) - Math.min(...boxes.map((n) => n.y));
  };

  let best = { bands: [columns], score: fitScale(span + 2 * MARGIN, heightOf(columns) + 2 * MARGIN) };
  for (let count = 2; count <= Math.min(4, columns.length); count++) {
    const bands: Column[][] = Array.from({ length: count }, () => []);
    for (const column of columns) {
      const centre = (column.x0 + column.x1) / 2 - columns[0].x0;
      bands[Math.min(count - 1, Math.floor((centre * count) / span))].push(column);
    }
    if (bands.some((band) => band.length === 0)) continue;
    const width = Math.max(...bands.map((band) => band.at(-1)!.x1 - band[0].x0));
    const height = bands.reduce((sum, band) => sum + heightOf(band), 0) + (count - 1) * RANK_SEP;
    const score = fitScale(width + 2 * MARGIN, height + 2 * MARGIN);
    if (score > best.score * 1.1) best = { bands, score };
  }
  if (best.bands.length === 1) return;

  const width = Math.max(...best.bands.map((band) => band.at(-1)!.x1 - band[0].x0));
  let top = MARGIN;
  best.bands.forEach((band, i) => {
    const boxes = band.flatMap((column) => column.nodes);
    const bandTop = Math.min(...boxes.map((n) => n.y));
    const height = heightOf(band);
    for (const node of boxes) {
      const offset = node.x - band[0].x0;
      node.x = i % 2 === 0 ? MARGIN + offset : MARGIN + width - offset - node.width;
      node.y = node.y - bandTop + top;
    }
    top += height + RANK_SEP;
  });
}

/** Shifts the tables so the topmost and leftmost sit on the margin. */
function moveToMargin(nodes: DiagramNode[]) {
  const dx = MARGIN - Math.min(...nodes.map((n) => n.x));
  const dy = MARGIN - Math.min(...nodes.map((n) => n.y));
  for (const node of nodes) {
    node.x += dx;
    node.y += dy;
  }
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
 * Tables no key joins go below the graph or beside it, whichever lets the
 * whole fit the frame larger: below in rows as wide as the graph, beside in
 * columns as tall as it, or either way a block a little wider than the
 * frame's proportions, since a row stops short of its bound. Stacked in
 * dagre's first rank they would make one column as tall as every table at
 * once.
 */
function placeLoose(nodes: DiagramNode[], graph: DiagramNode[]) {
  if (nodes.length === 0) return;
  const area = nodes.reduce((sum, node) => sum + (node.width + NODE_SEP) * (node.height + NODE_SEP), 0);
  if (graph.length === 0) {
    shelves(nodes, MARGIN, MARGIN + Math.sqrt(2 * area));
    return;
  }

  const right = Math.max(...graph.map((n) => n.x + n.width));
  const bottom = Math.max(...graph.map((n) => n.y + n.height));
  const below = () => shelves(nodes, bottom + RANK_SEP / 2, Math.max(right, MARGIN + Math.sqrt(2 * area)));
  const beside = () => stacks(nodes, right + RANK_SEP / 2, Math.max(bottom, MARGIN + Math.sqrt(area / 2)));
  const score = () => {
    const { width, height } = extent([...graph, ...nodes]);
    return fitScale(width, height);
  };

  beside();
  const besideScore = score();
  below();
  if (besideScore > score()) beside();
}

/** Rows from `top`, left to right, each starting again at the margin past `right`. */
function shelves(nodes: DiagramNode[], top: number, right: number) {
  let x = MARGIN;
  let y = top;
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
}

/** Columns from `left`, top to bottom, each starting again at the margin past `bottom`. */
function stacks(nodes: DiagramNode[], left: number, bottom: number) {
  let x = left;
  let y = MARGIN;
  let column = 0;
  for (const node of nodes) {
    if (y > MARGIN && y + node.height > bottom) {
      y = MARGIN;
      x += column + NODE_SEP;
      column = 0;
    }
    node.x = x;
    node.y = y;
    y += node.height + NODE_SEP;
    column = Math.max(column, node.width);
  }
}

/** An arrow's ends, on the facing sides of the two boxes. */
function link(
  from: DiagramNode,
  column: string,
  to: DiagramNode,
  referenced: string | undefined,
  nodes: DiagramNode[],
): DiagramLink {
  const y1 = rowMiddle(from, column);
  const y2 = rowMiddle(to, referenced);

  if (from !== to && to.x >= from.x + from.width + 2 * CLEARANCE) {
    const ends = { x1: from.x + from.width, y1, x2: to.x, y2 };
    return { ...ends, out1: 1, out2: -1, via: passages(ends, from, to, nodes) };
  }
  if (from !== to && to.x + to.width + 2 * CLEARANCE <= from.x) {
    const ends = { x1: from.x, y1, x2: to.x + to.width, y2 };
    return { ...ends, out1: -1, out2: 1, via: passages(ends, from, to, nodes) };
  }
  // The same box, or two in one column, or too close side by side for a run
  // between them: a loop out of the right side and back.
  return { x1: from.x + from.width, y1, x2: to.x + to.width, y2, out1: 1, out2: 1, via: [] };
}

/** How far an arrow passing a table keeps from its edge. */
const CLEARANCE = 12;

/**
 * The level runs an arrow takes, which dagre's own bend points would give
 * only for its layout, not after the boxes are moved. First, where its table
 * stands in a column of wider ones, a run along its own row until it is
 * clear of them, at either end: a curve straight from the side of a narrow
 * box would cut a wider neighbour's corner. Between those, a single curve
 * stays inside the rectangle its ends span, so it is kept when no other table
 * touches that rectangle. Otherwise each column of tables in the way, the
 * boxes overlapping in x merged into one, is crossed level at the free height
 * nearest the straight line, and the curves between bend only in the empty
 * gaps between columns.
 */
function passages(
  { x1, y1, x2, y2 }: { x1: number; y1: number; x2: number; y2: number },
  from: DiagramNode,
  to: DiagramNode,
  nodes: DiagramNode[],
): DiagramPassage[] {
  const rightward = x2 > x1;
  const others = nodes.filter((n) => n !== from && n !== to);
  let start = clearOfColumn(x1, y1, rightward ? 1 : -1, others);
  let end = clearOfColumn(x2, y2, rightward ? -1 : 1, others);
  if (rightward ? start >= end : start <= end) [start, end] = [x1, x2];

  const left = Math.min(start, end);
  const right = Math.max(start, end);
  const between = others.filter((n) => n.x < right && n.x + n.width > left);
  const top = Math.min(y1, y2);
  const bottom = Math.max(y1, y2);
  const runs: DiagramPassage[] = [];
  if (start !== x1) runs.push({ enter: x1, leave: start, y: y1 });
  if (between.some((n) => n.y < bottom && n.y + n.height > top)) {
    const columns = columnsOf(between);
    if (!rightward) columns.reverse();
    for (const column of columns) {
      // A column wider than the gap between the ends is crossed within the gap.
      const [lo, hi] = [Math.max(column.x0, left), Math.min(column.x1, right)];
      const share = ((lo + hi) / 2 - start) / (end - start);
      runs.push({
        enter: rightward ? lo : hi,
        leave: rightward ? hi : lo,
        y: freeHeight(column.nodes, y1 + share * (y2 - y1)),
      });
    }
  }
  if (end !== x2) runs.push({ enter: end, leave: x2, y: y2 });
  return runs;
}

/**
 * How far from `x`, going `direction`, a level run at `y` must go to be past
 * every box straddling its way: the wider tables above and below in its own
 * column. It stops short at a box that stands at `y` itself.
 */
function clearOfColumn(x: number, y: number, direction: 1 | -1, nodes: DiagramNode[]): number {
  let edge = x;
  for (;;) {
    const straddling = nodes.filter((n) => n.x < edge && n.x + n.width > edge);
    if (straddling.length === 0 || straddling.some((n) => n.y <= y && n.y + n.height >= y)) return edge;
    edge = direction > 0 ? Math.max(...straddling.map((n) => n.x + n.width)) : Math.min(...straddling.map((n) => n.x));
  }
}

/** The height nearest `wanted` that passes every box `CLEARANCE` clear. */
function freeHeight(boxes: DiagramNode[], wanted: number): number {
  const blocked = boxes
    .map((n) => [n.y - CLEARANCE, n.y + n.height + CLEARANCE] as const)
    .sort((a, b) => a[0] - b[0]);
  const merged: [number, number][] = [];
  for (const [start, end] of blocked) {
    const last = merged.at(-1);
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else merged.push([start, end]);
  }
  const hit = merged.find(([start, end]) => wanted > start && wanted < end);
  if (!hit) return wanted;
  return wanted - hit[0] <= hit[1] - wanted ? hit[0] : hit[1];
}

/** A column's row middle, or the header's when the box does not carry it. */
function rowMiddle(node: DiagramNode, column: string | undefined): number {
  const index = column === undefined ? -1 : node.columns.findIndex((c) => c.name === column);
  if (index < 0) return node.y + HEADER_HEIGHT / 2;
  return node.y + HEADER_HEIGHT + index * ROW_HEIGHT + ROW_HEIGHT / 2;
}

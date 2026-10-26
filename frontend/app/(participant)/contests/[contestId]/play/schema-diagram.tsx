"use client";

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Maximize, ZoomIn, ZoomOut } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { GameSchema } from "@/lib/api/schema";
import type { Dictionary } from "@/lib/i18n/dictionary";

import {
  HEADER_HEIGHT,
  MARK_WIDTH,
  PADDING,
  ROW_HEIGHT,
  bounds,
  layoutDiagram,
  linkPath,
  routeEdge,
  routeEdges,
  type Diagram,
  type DiagramNode,
} from "./schema-diagram-layout";
import { readPositions, schemaIdentity, writePositions, type Position, type Positions } from "./schema-diagram-positions";

type DiagramDictionary = Dictionary["participant"]["play"]["schema"]["diagram"];

type View = { x: number; y: number; k: number };

const MAX_ZOOM = 2;
const ZOOM_STEP = 1.25;
const PAN_STEP = 64;
/** How far an arrow key moves a focused table, in diagram units; Shift moves four times as far. */
const MOVE_STEP = 16;
/** Below this zoom column text is under 6px, so boxes are drawn without rows. */
const DETAIL_ZOOM = 0.45;
/** jsdom and a not-yet-laid-out dialog both measure 0. */
const FALLBACK_SIZE = { width: 960, height: 600 };

const ARROWS: Record<string, [number, number]> = {
  ArrowLeft: [-1, 0],
  ArrowRight: [1, 0],
  ArrowUp: [0, -1],
  ArrowDown: [0, 1],
};

/**
 * The game's schema drawn as boxes and arrows, panned and zoomed by pointer,
 * wheel and keyboard, its tables moved by dragging or by the arrow keys and
 * remembered per contest in this browser. Loaded lazily by the schema panel's
 * dialog, so dagre and this file stay off the play screen's first load. A
 * screen reader hears each table's name and reads the relations as a list.
 */
export default function SchemaDiagram({
  schema,
  contestId,
  t,
  truncatedNote,
}: {
  schema: GameSchema;
  /** Keys the remembered positions. */
  contestId: string;
  t: DiagramDictionary;
  truncatedNote: string;
}) {
  const diagram = useMemo(() => layoutDiagram(schema), [schema]);

  if (schema.tables.length === 0) {
    return <p className="font-mono text-data text-ink-3">{t.empty}</p>;
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      {schema.truncated ? <p className="font-mono text-label text-warn normal-case">{truncatedNote}</p> : null}
      {/* Another set of tables is another drawing: nothing moved in one applies to the next. */}
      <Canvas
        key={schemaIdentity(diagram.nodes.map((node) => node.name))}
        diagram={diagram}
        contestId={contestId}
        t={t}
      />
      <Relations schema={schema} t={t} />
    </div>
  );
}

type Drag =
  | { kind: "pan"; id: number; x: number; y: number }
  | { kind: "table"; id: number; name: string; startX: number; startY: number; from: Position };

function Canvas({ diagram, contestId, t }: { diagram: Diagram; contestId: string; t: DiagramDictionary }) {
  const frameRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState(FALLBACK_SIZE);
  const names = useMemo(() => diagram.nodes.map((node) => node.name), [diagram]);
  const [moved, setMoved] = useState<Positions>(() => readPositions(contestId, names));
  /** The table under the pointer and where it is now, until it is dropped. */
  const [dragged, setDragged] = useState<{ name: string } & Position | null>(null);
  /** The table with focus, drawn even when out of view so moving it never drops focus. */
  const [focused, setFocused] = useState<string | null>(null);

  const placed = useMemo(
    () =>
      moved.size === 0
        ? diagram.nodes
        : diagram.nodes.map((node) => {
            const position = moved.get(node.name);
            return position ? { ...node, ...position } : node;
          }),
    [diagram.nodes, moved],
  );
  const routed = useMemo(
    () => (moved.size === 0 ? diagram.edges : routeEdges(diagram.edges, placed)),
    [diagram.edges, moved.size, placed],
  );
  const byName = useMemo(() => new Map(placed.map((node) => [node.name, node])), [placed]);

  const fit = useMemo(() => fitView(bounds(placed), size), [placed, size]);
  const [view, setView] = useState<View | null>(null);
  const shown = view ?? fit;
  const minZoom = Math.min(0.1, fit.k);
  const helpId = useId();
  const markerId = useId();

  useLayoutEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const measure = () => {
      const { width, height } = frame.getBoundingClientRect();
      if (width > 0 && height > 0) setSize({ width, height });
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(frame);
    return () => observer.disconnect();
  }, []);

  /** Zooms by `factor` keeping the point (px, py) of the frame where it is. */
  const zoomAt = useCallback(
    (factor: number, px: number, py: number) =>
      setView((current) => {
        const from = current ?? fit;
        const k = clamp(from.k * factor, minZoom, MAX_ZOOM);
        return { k, x: px - ((px - from.x) * k) / from.k, y: py - ((py - from.y) * k) / from.k };
      }),
    [fit, minZoom],
  );
  const zoomCentre = (factor: number) => zoomAt(factor, size.width / 2, size.height / 2);
  const pan = (dx: number, dy: number) =>
    setView((current) => {
      const from = current ?? fit;
      return { ...from, x: from.x + dx, y: from.y + dy };
    });
  // A moved table changes the fitted view; holding the view where it is keeps
  // the drawing still under the pointer.
  const holdView = () => setView((current) => current ?? fit);

  function commit(next: Positions) {
    setMoved(next);
    writePositions(contestId, names, next);
  }
  function moveTable(name: string, position: Position) {
    commit(new Map(moved).set(name, position));
  }

  // React's wheel listener is passive, so it cannot stop the page scrolling.
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const box = frame.getBoundingClientRect();
      zoomAt(Math.exp(-event.deltaY * 0.0015), event.clientX - box.left, event.clientY - box.top);
    };
    frame.addEventListener("wheel", onWheel, { passive: false });
    return () => frame.removeEventListener("wheel", onWheel);
  }, [zoomAt]);

  const drag = useRef<Drag | null>(null);

  function onKeyDown(event: React.KeyboardEvent) {
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    const arrow = ARROWS[event.key];
    const table = tableOf(event.target);
    if (arrow && table) {
      event.preventDefault();
      const node = byName.get(table);
      if (!node) return;
      const step = event.shiftKey ? MOVE_STEP * 4 : MOVE_STEP;
      holdView();
      moveTable(table, { x: node.x + arrow[0] * step, y: node.y + arrow[1] * step });
      return;
    }
    const step = event.shiftKey ? PAN_STEP * 4 : PAN_STEP;
    const actions: Record<string, () => void> = {
      ArrowLeft: () => pan(step, 0),
      ArrowRight: () => pan(-step, 0),
      ArrowUp: () => pan(0, step),
      ArrowDown: () => pan(0, -step),
      "+": () => zoomCentre(ZOOM_STEP),
      "=": () => zoomCentre(ZOOM_STEP),
      "-": () => zoomCentre(1 / ZOOM_STEP),
      _: () => zoomCentre(1 / ZOOM_STEP),
      "0": () => setView(null),
    };
    const action = actions[event.key];
    if (!action) return;
    event.preventDefault();
    action();
  }

  function onPointerDown(event: React.PointerEvent<HTMLDivElement>) {
    if (event.button !== 0) return;
    const table = tableOf(event.target);
    const node = table ? byName.get(table) : undefined;
    if (table && node) {
      drag.current = {
        kind: "table",
        id: event.pointerId,
        name: table,
        startX: event.clientX,
        startY: event.clientY,
        from: { x: node.x, y: node.y },
      };
      holdView();
    } else {
      drag.current = { kind: "pan", id: event.pointerId, x: event.clientX, y: event.clientY };
    }
    event.currentTarget.setPointerCapture?.(event.pointerId);
  }

  function onPointerMove(event: React.PointerEvent<HTMLDivElement>) {
    const current = drag.current;
    if (!current || current.id !== event.pointerId) return;
    if (current.kind === "pan") {
      drag.current = { ...current, x: event.clientX, y: event.clientY };
      pan(event.clientX - current.x, event.clientY - current.y);
      return;
    }
    setDragged({
      name: current.name,
      x: Math.round(current.from.x + (event.clientX - current.startX) / shown.k),
      y: Math.round(current.from.y + (event.clientY - current.startY) / shown.k),
    });
  }

  function onPointerEnd(drop: boolean) {
    const current = drag.current;
    drag.current = null;
    if (current?.kind !== "table" || !dragged) return;
    setDragged(null);
    if (drop && (dragged.x !== current.from.x || dragged.y !== current.from.y)) {
      moveTable(dragged.name, { x: dragged.x, y: dragged.y });
    }
  }

  // While a table is dragged only its own arrows are routed again, each move;
  // the drop routes them all, past the table where it now stands.
  let nodes = placed;
  let edges = routed;
  if (dragged) {
    nodes = placed.map((node) => (node.name === dragged.name ? { ...node, x: dragged.x, y: dragged.y } : node));
    const live = new Map(nodes.map((node) => [node.name, node]));
    edges = routed.map((edge) =>
      edge.from === dragged.name || edge.to === dragged.name ? routeEdge(edge, live, nodes) : edge,
    );
  }

  const visible = {
    left: -shown.x / shown.k,
    top: -shown.y / shown.k,
    right: (size.width - shown.x) / shown.k,
    bottom: (size.height - shown.y) / shown.k,
  };
  const detailed = shown.k >= DETAIL_ZOOM;
  const drawn = nodes.filter(
    (node) =>
      node.name === focused ||
      node.name === dragged?.name ||
      (node.x <= visible.right &&
        node.x + node.width >= visible.left &&
        node.y <= visible.bottom &&
        node.y + node.height >= visible.top),
  );

  return (
    <div className="relative flex min-h-0 flex-1 flex-col border border-line">
      <p id={helpId} className="sr-only">
        {t.keys}
      </p>
      <div
        ref={frameRef}
        role="application"
        aria-label={t.canvas}
        aria-describedby={helpId}
        tabIndex={0}
        onKeyDown={onKeyDown}
        onFocus={(event) => setFocused(tableOf(event.target))}
        onBlur={() => setFocused(null)}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={() => onPointerEnd(true)}
        onPointerCancel={() => onPointerEnd(false)}
        className="min-h-0 flex-1 cursor-grab touch-none overflow-hidden bg-bg select-none active:cursor-grabbing"
      >
        <svg className="block size-full">
          <defs aria-hidden="true">
            <marker
              id={markerId}
              viewBox="0 0 10 10"
              refX="10"
              refY="5"
              markerWidth="8"
              markerHeight="8"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" className="fill-ink-3" />
            </marker>
          </defs>
          <g data-viewport="" transform={`translate(${shown.x} ${shown.y}) scale(${shown.k})`}>
            <g aria-hidden="true">
              {edges.map((edge) => (
                <g key={edge.id} data-from={edge.from} data-edge={edge.name} className="fill-none stroke-ink-3">
                  {edge.links.map((link, i) => (
                    <path
                      key={i}
                      d={linkPath(link)}
                      markerEnd={`url(#${markerId})`}
                      strokeWidth={1.25}
                      vectorEffect="non-scaling-stroke"
                    />
                  ))}
                </g>
              ))}
            </g>
            {drawn.map((node) => (
              <TableBox key={node.name} node={node} detailed={detailed} t={t} />
            ))}
            {/* SVG paints in document order, and moving the held box last would
                take focus from it: a copy is drawn over the tables it passes. */}
            {dragged ? <TableBox node={nodes.find((node) => node.name === dragged.name)!} detailed={detailed} t={t} copy /> : null}
          </g>
        </svg>
      </div>
      {/* After the canvas, so Tab reaches the drawing first. */}
      <div className="absolute top-2 right-2 z-10 flex items-center gap-1 border border-line bg-panel p-1">
        <Button
          variant="quiet"
          size="sm"
          onClick={() => {
            commit(new Map());
            setView(null);
          }}
          title={t.arrangeHint}
        >
          {t.arrange}
        </Button>
        <Button variant="quiet" size="icon" onClick={() => zoomCentre(1 / ZOOM_STEP)} aria-label={t.zoomOut} title={t.zoomOut}>
          <ZoomOut aria-hidden="true" />
        </Button>
        <span className="w-12 text-center font-mono text-label text-ink-3" aria-hidden="true">
          {Math.round(shown.k * 100)}%
        </span>
        <Button variant="quiet" size="icon" onClick={() => zoomCentre(ZOOM_STEP)} aria-label={t.zoomIn} title={t.zoomIn}>
          <ZoomIn aria-hidden="true" />
        </Button>
        <Button variant="quiet" size="icon" onClick={() => setView(null)} aria-label={t.fit} title={t.fit}>
          <Maximize aria-hidden="true" />
        </Button>
      </div>
    </div>
  );
}

/** The name of the table an event happened on, or null on the background. */
function tableOf(target: EventTarget): string | null {
  return target instanceof Element ? (target.closest("[data-table]")?.getAttribute("data-table") ?? null) : null;
}

/**
 * One table, focusable so the arrow keys can move it. Its name is all a
 * screen reader hears of it: the columns and keys are in the lists below the
 * drawing, which say them as sentences.
 */
function TableBox({
  node,
  detailed,
  t,
  copy = false,
}: {
  node: DiagramNode;
  detailed: boolean;
  t: DiagramDictionary;
  /** A picture of the box only, drawn over the others while it is dragged. */
  copy?: boolean;
}) {
  const box = copy
    ? { "data-copy": node.name, "aria-hidden": true, className: "pointer-events-none font-mono text-data" }
    : {
        "data-table": node.name,
        tabIndex: 0,
        role: "group",
        "aria-label": node.name,
        "aria-roledescription": t.table,
        className: "group cursor-move font-mono text-data outline-none",
      };
  return (
    <g transform={`translate(${node.x} ${node.y})`} {...box}>
      <g aria-hidden="true">
        <rect
          width={node.width}
          height={node.height}
          className="fill-panel stroke-line-2 group-focus-visible:stroke-accent group-focus-visible:stroke-2"
        />
        <rect width={node.width} height={HEADER_HEIGHT} className="fill-sunk stroke-line-2" />
        <text x={PADDING} y={HEADER_HEIGHT / 2} dominantBaseline="central" className="fill-ink font-medium">
          {node.label}
        </text>
        {detailed
          ? node.columns.map((column, i) => (
              <g
                key={column.name}
                data-column={column.name}
                transform={`translate(0 ${HEADER_HEIGHT + i * ROW_HEIGHT + ROW_HEIGHT / 2})`}
              >
                {column.primary || column.foreign ? (
                  <text
                    data-mark=""
                    x={PADDING}
                    dominantBaseline="central"
                    className={column.primary ? "fill-accent text-label" : "fill-ink-3 text-label"}
                  >
                    {column.primary ? "PK" : "FK"}
                  </text>
                ) : null}
                <text x={PADDING + MARK_WIDTH} dominantBaseline="central" className="fill-ink-2">
                  {column.label}
                </text>
                <text x={node.width - PADDING} dominantBaseline="central" textAnchor="end" className="fill-ink-3">
                  {column.typeLabel}
                </text>
              </g>
            ))
          : null}
      </g>
    </g>
  );
}

/** The relations and primary keys as lists, for a screen reader. */
function Relations({ schema, t }: { schema: GameSchema; t: DiagramDictionary }) {
  const relations = schema.tables.flatMap((table) =>
    (table.foreign_keys ?? []).map((key) => ({
      id: `${table.name}\u0000${key.name}`,
      text: t.relation
        .replace("{table}", table.name)
        .replace("{columns}", key.columns.join(", "))
        .replace("{target}", key.referenced_table)
        .replace("{targetColumns}", key.referenced_columns.join(", ")),
    })),
  );
  const keyed = schema.tables.filter((table) => (table.primary_key ?? []).length > 0);
  const relationsId = useId();
  const keysId = useId();

  return (
    <section className="sr-only">
      <h3 id={relationsId}>{t.relations}</h3>
      {relations.length > 0 ? (
        <ul aria-labelledby={relationsId}>
          {relations.map((relation) => (
            <li key={relation.id}>{relation.text}</li>
          ))}
        </ul>
      ) : (
        <p>{t.noRelations}</p>
      )}
      {keyed.length > 0 ? (
        <>
          <h3 id={keysId}>{t.primaryKeys}</h3>
          <ul aria-labelledby={keysId}>
            {keyed.map((table) => (
              <li key={table.name}>
                {t.primaryKey.replace("{table}", table.name).replace("{columns}", (table.primary_key ?? []).join(", "))}
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </section>
  );
}

/** The whole drawing centred in the frame, never enlarged past 100%. */
function fitView(
  { left, top, right, bottom }: ReturnType<typeof bounds>,
  size: { width: number; height: number },
): View {
  const [width, height] = [right - left, bottom - top];
  if (width === 0 || height === 0) return { x: 0, y: 0, k: 1 };
  const k = Math.min(1, size.width / width, size.height / height);
  return { k, x: (size.width - width * k) / 2 - left * k, y: (size.height - height * k) / 2 - top * k };
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

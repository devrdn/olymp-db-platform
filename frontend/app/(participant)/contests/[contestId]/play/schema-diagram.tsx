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
  layoutDiagram,
  linkPath,
  type Diagram,
  type DiagramNode,
} from "./schema-diagram-layout";

type DiagramDictionary = Dictionary["participant"]["play"]["schema"]["diagram"];

type View = { x: number; y: number; k: number };

const MAX_ZOOM = 2;
const ZOOM_STEP = 1.25;
const PAN_STEP = 64;
/** Below this zoom column text is under 6px, so boxes are drawn without rows. */
const DETAIL_ZOOM = 0.45;
/** jsdom and a not-yet-laid-out dialog both measure 0. */
const FALLBACK_SIZE = { width: 960, height: 600 };

/**
 * The game's schema drawn as boxes and arrows, panned and zoomed by pointer,
 * wheel and keyboard. Loaded lazily by the schema panel's dialog, so dagre
 * and this file stay off the play screen's first load. The drawing is hidden
 * from assistive technology, which reads the relations as a list instead.
 */
export default function SchemaDiagram({
  schema,
  t,
  truncatedNote,
}: {
  schema: GameSchema;
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
      <Canvas diagram={diagram} t={t} />
      <Relations schema={schema} t={t} />
    </div>
  );
}

function Canvas({ diagram, t }: { diagram: Diagram; t: DiagramDictionary }) {
  const frameRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState(FALLBACK_SIZE);
  const fit = useMemo(() => fitView(diagram, size), [diagram, size]);
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

  const drag = useRef<{ id: number; x: number; y: number } | null>(null);

  function onKeyDown(event: React.KeyboardEvent) {
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
    if (!action || event.metaKey || event.ctrlKey || event.altKey) return;
    event.preventDefault();
    action();
  }

  const visible = {
    left: -shown.x / shown.k,
    top: -shown.y / shown.k,
    right: (size.width - shown.x) / shown.k,
    bottom: (size.height - shown.y) / shown.k,
  };
  const detailed = shown.k >= DETAIL_ZOOM;
  const nodes = diagram.nodes.filter(
    (node) =>
      node.x <= visible.right &&
      node.x + node.width >= visible.left &&
      node.y <= visible.bottom &&
      node.y + node.height >= visible.top,
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
        onPointerDown={(event) => {
          if (event.button !== 0) return;
          drag.current = { id: event.pointerId, x: event.clientX, y: event.clientY };
          event.currentTarget.setPointerCapture?.(event.pointerId);
        }}
        onPointerMove={(event) => {
          const from = drag.current;
          if (!from || from.id !== event.pointerId) return;
          drag.current = { id: from.id, x: event.clientX, y: event.clientY };
          pan(event.clientX - from.x, event.clientY - from.y);
        }}
        onPointerUp={() => {
          drag.current = null;
        }}
        onPointerCancel={() => {
          drag.current = null;
        }}
        className="min-h-0 flex-1 cursor-grab touch-none overflow-hidden bg-bg select-none active:cursor-grabbing"
      >
        <svg aria-hidden="true" className="block size-full">
          <defs>
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
            {diagram.edges.map((edge) => (
              <g key={edge.id} data-edge={edge.id} className="fill-none stroke-ink-3">
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
            {nodes.map((node) => (
              <TableBox key={node.name} node={node} detailed={detailed} />
            ))}
          </g>
        </svg>
      </div>
      {/* After the canvas, so Tab reaches the drawing first. */}
      <div className="absolute top-2 right-2 z-10 flex items-center gap-1 border border-line bg-panel p-1">
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

function TableBox({ node, detailed }: { node: DiagramNode; detailed: boolean }) {
  return (
    <g data-table={node.name} transform={`translate(${node.x} ${node.y})`} className="font-mono text-data">
      <rect width={node.width} height={node.height} className="fill-panel stroke-line-2" />
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
  );
}

/** The relations and primary keys as lists, for a screen reader. */
function Relations({ schema, t }: { schema: GameSchema; t: DiagramDictionary }) {
  const relations = schema.tables.flatMap((table) =>
    (table.foreign_keys ?? []).map((key) => ({
      id: `${table.name}.${key.name}`,
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
function fitView(diagram: Diagram, size: { width: number; height: number }): View {
  if (diagram.width === 0 || diagram.height === 0) return { x: 0, y: 0, k: 1 };
  const k = Math.min(1, size.width / diagram.width, size.height / diagram.height);
  return { k, x: (size.width - diagram.width * k) / 2, y: (size.height - diagram.height * k) / 2 };
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

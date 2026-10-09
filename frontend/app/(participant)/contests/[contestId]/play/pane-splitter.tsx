"use client";

import { useCallback, useRef, useSyncExternalStore } from "react";

import { cn } from "@/lib/utils";

/**
 * The console's draggable edges and the sizes they leave behind (SPEC.md §5,
 * and §11's `ConsoleShell`). How much room the questions or the result need
 * depends on the contest and the participant, so no split is fixed. One
 * store and one handle serve every edge; axis, unit and bounds are data.
 *
 * A drag does not go through React: pointermove writes the size onto the
 * container as a custom property, one style recalculation, where state would
 * re-render the editor, the result table and every question per event
 * (SPEC.md §6 asks for 120ms). State is written, and the size stored, once on
 * release.
 */

/**
 * Per contest, since a wide-question olympiad and a narrow one are different
 * screens. The group keeps widths and shares in separate records, so a new
 * split needs no migration.
 */
function storageKey(group: string, contestId: string) {
  return `dbcontest.console.${group}.${contestId}`;
}

/** What a size may be clamped to, in that size's own unit. */
export type PaneBounds = { min: number; max: number };

/** The design's starting widths (docs/design/preview.html): 212px and 252px. */
export const DEFAULT_SCHEMA_REM = 13.25;
export const DEFAULT_SIDE_REM = 15.75;

/**
 * Column width limits in rem. A hard floor: below it the schema shows no
 * column names and the questions wrap every other word.
 */
export const WIDTH_BOUNDS: PaneBounds = { min: 8, max: 32 };

/**
 * The editor's share of the console column, in percent. A share, not a
 * length: the column is as tall as the viewport, and the screens differ.
 */
export const DEFAULT_EDITOR_PCT = 55;

/**
 * The limits on a share, in percent: below a fifth of the column the editor
 * holds about two lines and the result only its header.
 */
export const SHARE_BOUNDS: PaneBounds = { min: 20, max: 80 };

type Sizes<K extends string> = Record<K, number>;

function clampTo(value: number, bounds: PaneBounds): number {
  return Math.min(bounds.max, Math.max(bounds.min, value));
}

/** One stored group, or the defaults when it cannot be read. */
function parse<K extends string>(raw: string | null, defaults: Sizes<K>, bounds: PaneBounds): Sizes<K> {
  const value = { ...defaults };
  if (!raw) return value;
  try {
    const parsed = JSON.parse(raw) as Partial<Record<K, unknown>>;
    for (const key of Object.keys(defaults) as K[]) {
      const stored = parsed[key];
      if (typeof stored === "number" && Number.isFinite(stored)) {
        value[key] = clampTo(stored, bounds);
      }
    }
  } catch {
    return { ...defaults };
  }
  return value;
}

/**
 * The stored sizes as an external store: storage read during render would
 * mismatch hydration, and an effect would render the console twice. The
 * server snapshot is the defaults. The snapshot must be referentially stable,
 * so the parsed group is cached against its raw string.
 */
const cache = new Map<string, { raw: string | null; value: Sizes<string> }>();

function snapshot<K extends string>(
  group: string,
  contestId: string,
  defaults: Sizes<K>,
  bounds: PaneBounds,
): Sizes<K> {
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(storageKey(group, contestId));
  } catch {
    raw = null;
  }

  const id = `${group}:${contestId}`;
  const cached = cache.get(id);
  if (cached && cached.raw === raw) return cached.value as Sizes<K>;

  const value = parse(raw, defaults, bounds);
  cache.set(id, { raw, value });
  return value;
}

/** Lets a commit in this tab re-render without waiting for a storage event. */
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  // Another tab's divider is not followed live, but the storage event keeps
  // the cached snapshot from fighting it.
  const onStorage = () => listener();
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", onStorage);
  };
}

/**
 * One group of remembered sizes and the container they are written on.
 * `defaults` and `bounds` must be module constants: the server snapshot is
 * compared by identity, and a fresh literal would be a fresh snapshot.
 */
export function usePaneSizes<K extends string>(
  contestId: string,
  group: string,
  defaults: Sizes<K>,
  bounds: PaneBounds,
) {
  const containerRef = useRef<HTMLDivElement>(null);
  const sizes = useSyncExternalStore(
    subscribe,
    () => snapshot(group, contestId, defaults, bounds),
    () => defaults,
  );

  const commit = useCallback(
    (next: Sizes<K>) => {
      const raw = JSON.stringify(next);
      try {
        window.localStorage.setItem(storageKey(group, contestId), raw);
      } catch {
        // Refused storage costs nothing this session: the container already
        // carries the size.
      }
      cache.set(`${group}:${contestId}`, { raw, value: next });
      for (const listener of listeners) listener();
    },
    [group, contestId],
  );

  return { containerRef, sizes, commit };
}

const PANE_WIDTHS: Sizes<"schema" | "side"> = {
  schema: DEFAULT_SCHEMA_REM,
  side: DEFAULT_SIDE_REM,
};

/** The two column widths, in rem. The `panes` key predates the other groups. */
export function usePaneWidths(contestId: string) {
  return usePaneSizes(contestId, "panes", PANE_WIDTHS, WIDTH_BOUNDS);
}

const CONSOLE_ROWS: Sizes<"editor"> = { editor: DEFAULT_EDITOR_PCT };

/** The editor's share of the console column, as a percentage. */
export function useConsoleRows(contestId: string) {
  return usePaneSizes(contestId, "rows", CONSOLE_ROWS, SHARE_BOUNDS);
}

/**
 * The table's share of the result pane while a row is open under it: the
 * table is navigated, the row only read.
 */
export const DEFAULT_RESULT_TABLE_PCT = 65;

const RESULT_ROWS: Sizes<"table"> = { table: DEFAULT_RESULT_TABLE_PCT };

export function useResultRows(contestId: string) {
  return usePaneSizes(contestId, "result", RESULT_ROWS, SHARE_BOUNDS);
}

/**
 * One draggable edge: `role="separator"` with `aria-valuenow` and arrow keys,
 * so it can be moved without a mouse. `axis` is the pointer's direction, so
 * an `x` edge is a vertical rule (`aria-orientation`).
 */
export function PaneHandle({
  label,
  property,
  value,
  axis = "x",
  unit = "rem",
  bounds,
  direction,
  containerRef,
  onResize,
  className,
}: {
  /** The accessible name, in the participant's language. */
  label: string;
  /** The custom property this handle drives; never derived from the translated label. */
  property: string;
  value: number;
  /** Which way the pointer travels to move this edge. */
  axis?: "x" | "y";
  /** What `value` is measured in, and what is written onto the container. */
  unit?: "rem" | "%";
  bounds: PaneBounds;
  /** Which way the pointer moves to make the pane this handle sizes larger. */
  direction: 1 | -1;
  containerRef: React.RefObject<HTMLDivElement | null>;
  onResize: (value: number) => void;
  className?: string;
}) {
  const dragging = useRef<{ start: number; startValue: number } | null>(null);

  // Pixels per unit, read once when the drag starts: reading layout on each
  // event would flush the previous write and recalculate the grid every
  // time. Neither the root font size nor the container changes mid-drag.
  const scaleRef = useRef(16);

  const vertical = axis === "x";
  const decrease = vertical ? "ArrowLeft" : "ArrowUp";
  const increase = vertical ? "ArrowRight" : "ArrowDown";

  const pointAt = (event: React.PointerEvent) => (vertical ? event.clientX : event.clientY);

  /** Ends an interrupted drag, restoring the size in force. */
  const cancelDrag = () => {
    const had = dragging.current;
    dragging.current = null;
    if (had) containerRef.current?.style.setProperty(property, `${had.startValue}${unit}`);
  };

  const movedTo = (event: React.PointerEvent) => {
    const drag = dragging.current;
    if (!drag) return value;
    const moved = ((pointAt(event) - drag.start) / scaleRef.current) * direction;
    return clampTo(drag.startValue + moved, bounds);
  };

  return (
    <div
      role="separator"
      aria-label={label}
      aria-orientation={vertical ? "vertical" : "horizontal"}
      aria-valuenow={Math.round(value)}
      aria-valuemin={bounds.min}
      aria-valuemax={bounds.max}
      tabIndex={0}
      className={cn(
        // A hairline with a grab area that takes no layout space.
        "relative shrink-0 bg-line",
        vertical ? "w-px cursor-col-resize" : "h-px cursor-row-resize",
        // 9px of grab area for a mouse, 25px for a finger (`pointer-coarse`).
        // Not both: the wider area steals clicks from the panes on either side.
        "after:absolute after:content-['']",
        vertical
          ? "after:inset-y-0 after:-left-1 after:-right-1 pointer-coarse:after:-left-3 pointer-coarse:after:-right-3"
          : "after:inset-x-0 after:-top-1 after:-bottom-1 pointer-coarse:after:-top-3 pointer-coarse:after:-bottom-3",
        "hover:bg-line-2 focus-visible:bg-accent focus-visible:outline-none",
        className,
      )}
      onKeyDown={(event) => {
        const step = event.shiftKey ? 4 : 1;
        if (event.key === decrease) {
          event.preventDefault();
          onResize(clampTo(value - step * direction, bounds));
        } else if (event.key === increase) {
          event.preventDefault();
          onResize(clampTo(value + step * direction, bounds));
        }
      }}
      onPointerDown={(event) => {
        event.currentTarget.setPointerCapture(event.pointerId);
        if (unit === "rem") {
          scaleRef.current = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
        } else {
          // A point of share is a hundredth of the container.
          const box = containerRef.current?.getBoundingClientRect();
          const span = (vertical ? box?.width : box?.height) ?? 0;
          scaleRef.current = Math.max(1, span / 100);
        }
        dragging.current = { start: pointAt(event), startValue: value };
      }}
      onPointerMove={(event) => {
        const container = containerRef.current;
        if (!dragging.current || !container) return;

        // Straight onto the DOM; state waits for release (see the file doc).
        container.style.setProperty(property, `${movedTo(event)}${unit}`);
      }}
      onPointerUp={(event) => {
        const next = movedTo(event);
        const had = dragging.current;
        dragging.current = null;
        if (had && containerRef.current) onResize(next);
      }}
      // A touch drag can end without a pointerup (a second finger, the browser
      // taking it as a scroll), leaving a drag nothing ends and an uncommitted
      // size. Either event may arrive, so both cancel; after a normal release
      // there is no drag left, so losing capture undoes nothing.
      onPointerCancel={cancelDrag}
      onLostPointerCapture={cancelDrag}
    />
  );
}

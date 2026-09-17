"use client";

import { useCallback, useRef, useSyncExternalStore } from "react";

import { cn } from "@/lib/utils";

/**
 * The console's draggable edges and the sizes they leave behind — SPEC.md
 * §11's `ConsoleShell`, and §7 of the workspace design for the two horizontal
 * ones.
 *
 * A fixed layout was shipped first and the reason it did not last is the one
 * §11 anticipated: how much room the questions need is a property of the
 * contest, not of the product. An olympiad whose questions are two lines and
 * one whose questions are a paragraph want different columns, and neither
 * number is mine to pick. The same argument decides the horizontal edges:
 * reading a forty-column row and writing a fifteen-line query want opposite
 * splits of the console's own height.
 *
 * One store and one handle serve all of them. What varies is stated as data
 * rather than as a second copy of the file: which axis the pointer moves
 * along, what unit the number is in, and what it may be clamped to.
 *
 * The drag does not go through React. A pointermove writes the size straight
 * onto the container as a custom property, which is one style recalculation;
 * putting it in state would be a render of the editor, the result table and
 * every question card per pointer event, and SPEC.md §6 asks this screen to
 * react within 120ms. State is written once, on release, and that is also
 * when the size is stored.
 */

/**
 * Where a group of sizes lives between visits. Per contest is deliberate: a
 * wide-question olympiad and a narrow one are different screens.
 *
 * The group is part of the key so the widths and the shares are separate
 * records — a build that learns a new split does not have to migrate the one
 * already stored, and a stored value it cannot read falls back on its own.
 */
function storageKey(group: string, contestId: string) {
  return `dbcontest.console.${group}.${contestId}`;
}

/** What a size may be clamped to, in that size's own unit. */
export type PaneBounds = { min: number; max: number };

/** The design's own starting widths (docs/design/preview.html): 212px and 252px. */
export const DEFAULT_SCHEMA_REM = 13.25;
export const DEFAULT_SIDE_REM = 15.75;

/**
 * What a column may be narrowed to before it stops being a pane.
 *
 * A hard floor rather than a percentage: below this the schema tree shows no
 * column names and the questions wrap every second word, and a participant
 * who dragged too far in a hurry should not have to drag back to read
 * anything.
 */
export const WIDTH_BOUNDS: PaneBounds = { min: 8, max: 32 };

/**
 * The editor's share of the console column, as a percentage — the 11:9 the
 * layout used to state as grid fractions, now a number somebody can move.
 *
 * A share rather than a length, because this column is as tall as the
 * viewport: a stored `rem` would mean a different split on every machine the
 * contest is sat in front of, and the classroom's screens are not one size.
 */
export const DEFAULT_EDITOR_PCT = 55;

/**
 * Neither of the two panes may be squeezed out of existence: below a fifth of
 * the column the editor holds about two lines of SQL, and the result below it
 * holds a header and nothing under it.
 */
export const SHARE_BOUNDS: PaneBounds = { min: 20, max: 80 };

type Sizes<K extends string> = Record<K, number>;

function clampTo(value: number, bounds: PaneBounds): number {
  return Math.min(bounds.max, Math.max(bounds.min, value));
}

/** One stored group, or the defaults — a stored value this build cannot read is not a reason to fail. */
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
 * The stored sizes, as an external store.
 *
 * `useSyncExternalStore` rather than an effect that writes state: the server
 * has no localStorage, so a size read during render would be a hydration
 * mismatch — and reading it in an effect is a second render of the whole
 * console on every visit, which React's own lint rule refuses for exactly
 * that reason. The server snapshot is the design's defaults, the client
 * snapshot is what was stored, and React reconciles the two once.
 *
 * The snapshot has to be referentially stable or React re-renders forever, so
 * the parsed group is cached against the raw string it came from.
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

/** Listeners, so a commit in this tab re-renders without a round trip through storage events. */
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  // Another tab of the same contest moving its own divider is not this tab's
  // business to follow live, but a reload should not undo it either — the
  // storage event keeps the two from fighting.
  const onStorage = () => listener();
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", onStorage);
  };
}

/**
 * One group of remembered sizes and the container they are written on.
 *
 * `defaults` and `bounds` are read on every render and must therefore be the
 * same objects every time — module constants, not literals written at the
 * call site. React compares the store's snapshot by identity, and a fresh
 * defaults object would be a fresh snapshot on a server render.
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
        // A browser refusing storage costs the participant nothing this
        // session: the drag has already happened, and the container still
        // carries the size the pointer left it at.
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

/** The two column widths, in rem — the oldest group, and the one whose storage key predates the others. */
export function usePaneWidths(contestId: string) {
  return usePaneSizes(contestId, "panes", PANE_WIDTHS, WIDTH_BOUNDS);
}

const CONSOLE_ROWS: Sizes<"editor"> = { editor: DEFAULT_EDITOR_PCT };

/** The editor's share of the console column, as a percentage. */
export function useConsoleRows(contestId: string) {
  return usePaneSizes(contestId, "rows", CONSOLE_ROWS, SHARE_BOUNDS);
}

/**
 * One draggable edge.
 *
 * `role="separator"` with `aria-valuenow` and arrow keys, because a divider
 * that can only be moved with a mouse is a divider half the room cannot move
 * — and these ones decide how much of the screen the questions and the answer
 * get.
 *
 * `axis` is which way the pointer travels, so the separator *line* is the
 * other way round: an edge between two columns is dragged along `x` and reads
 * as a vertical rule, which is what `aria-orientation` names.
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
  /** The custom property this handle drives — never derived from the label, which is translated. */
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

  // How many pixels one unit is, read once when the drag starts rather than
  // on every pointer event. `getComputedStyle` and `getBoundingClientRect`
  // are both synchronous style reads, and reading on the next event flushes
  // the write from the previous one — a forced recalculation of a grid whose
  // middle column may hold a thousand-row table, per pointer event. Neither
  // the root font size nor the container's own size changes mid-drag.
  const scaleRef = useRef(16);

  const vertical = axis === "x";
  const decrease = vertical ? "ArrowLeft" : "ArrowUp";
  const increase = vertical ? "ArrowRight" : "ArrowDown";

  const pointAt = (event: React.PointerEvent) => (vertical ? event.clientX : event.clientY);

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
        // A hairline that widens to a grab area without taking layout space:
        // the pane it separates is the thing, not the handle.
        "relative shrink-0 bg-line",
        vertical ? "w-px cursor-col-resize" : "h-px cursor-row-resize",
        // Nine pixels for a mouse, twenty-five for a finger. Measured, the
        // handle is 1px wide and its grab area was 9px at every size — fine
        // for a pointer that lands where it is aimed, and not a target a
        // thumb can find on the tablets these dividers are visible on
        // from 760px up. The wider area is behind `pointer-coarse` rather
        // than applied to both, because it is not free: it is twelve pixels
        // of the pane on either side that stop taking a click of their own,
        // which is a real cost next to a result table's first column and the
        // questions' own text. A finger already loses that much to its own
        // contact patch; a mouse should not have to.
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
          // A point of share is a hundredth of the container it is a share
          // of, so the same arithmetic serves both units.
          const box = containerRef.current?.getBoundingClientRect();
          const span = (vertical ? box?.width : box?.height) ?? 0;
          scaleRef.current = Math.max(1, span / 100);
        }
        dragging.current = { start: pointAt(event), startValue: value };
      }}
      onPointerMove={(event) => {
        const container = containerRef.current;
        if (!dragging.current || !container) return;

        // Straight onto the DOM: see this file's own doc for why this does not
        // go through state until the pointer is released.
        container.style.setProperty(property, `${movedTo(event)}${unit}`);
      }}
      onPointerUp={(event) => {
        const next = movedTo(event);
        const had = dragging.current;
        dragging.current = null;
        if (had && containerRef.current) onResize(next);
      }}
    />
  );
}

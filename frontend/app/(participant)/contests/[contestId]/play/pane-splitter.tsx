"use client";

import { useCallback, useRef, useSyncExternalStore } from "react";

import { cn } from "@/lib/utils";

/**
 * The console's three panes, with the two edges between them draggable and
 * the widths remembered — SPEC.md §11's `ConsoleShell`.
 *
 * A fixed layout was shipped first and the reason it did not last is the one
 * §11 anticipated: how much room the questions need is a property of the
 * contest, not of the product. An olympiad whose questions are two lines and
 * one whose questions are a paragraph want different columns, and neither
 * number is mine to pick.
 *
 * The drag does not go through React. A pointermove writes the two widths
 * straight onto the container as custom properties, which is one style
 * recalculation; putting them in state would be a render of the editor, the
 * result table and every question card per pointer event, and SPEC.md §6 asks
 * this screen to react within 120ms. State is written once, on release, and
 * that is also when the sizes are stored.
 */

/** Where the widths live between visits. Per contest is deliberate: a wide-question olympiad and a narrow one are different screens. */
function storageKey(contestId: string) {
  return `dbcontest.console.panes.${contestId}`;
}

/** The design's own starting widths (docs/design/preview.html): 212px and 252px. */
export const DEFAULT_SCHEMA_REM = 13.25;
export const DEFAULT_SIDE_REM = 15.75;

/**
 * What a pane may be narrowed to before it stops being a pane.
 *
 * A hard floor rather than a percentage: below this the schema tree shows no
 * column names and the questions wrap every second word, and a participant
 * who dragged too far in a hurry should not have to drag back to read
 * anything.
 */
const MIN_REM = 8;
const MAX_REM = 32;

type Widths = { schema: number; side: number };

function clamp(rem: number): number {
  return Math.min(MAX_REM, Math.max(MIN_REM, rem));
}

/** One stored pair, or the defaults — a stored value this build cannot read is not a reason to fail. */
function parse(raw: string | null): Widths {
  if (!raw) return { schema: DEFAULT_SCHEMA_REM, side: DEFAULT_SIDE_REM };
  try {
    const parsed = JSON.parse(raw) as Partial<Widths>;
    return {
      schema: typeof parsed.schema === "number" ? clamp(parsed.schema) : DEFAULT_SCHEMA_REM,
      side: typeof parsed.side === "number" ? clamp(parsed.side) : DEFAULT_SIDE_REM,
    };
  } catch {
    return { schema: DEFAULT_SCHEMA_REM, side: DEFAULT_SIDE_REM };
  }
}

/**
 * The stored widths, as an external store.
 *
 * `useSyncExternalStore` rather than an effect that writes state: the server
 * has no localStorage, so a width read during render would be a hydration
 * mismatch — and reading it in an effect is a second render of the whole
 * console on every visit, which React's own lint rule refuses for exactly
 * that reason. The server snapshot is the design's defaults, the client
 * snapshot is what was stored, and React reconciles the two once.
 *
 * The snapshot has to be referentially stable or React re-renders forever, so
 * the parsed pair is cached against the raw string it came from.
 */
const cache = new Map<string, { raw: string | null; value: Widths }>();

function snapshot(contestId: string): Widths {
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(storageKey(contestId));
  } catch {
    raw = null;
  }

  const cached = cache.get(contestId);
  if (cached && cached.raw === raw) return cached.value;

  const value = parse(raw);
  cache.set(contestId, { raw, value });
  return value;
}

const DEFAULTS: Widths = { schema: DEFAULT_SCHEMA_REM, side: DEFAULT_SIDE_REM };

function serverSnapshot(): Widths {
  return DEFAULTS;
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

export function usePaneWidths(contestId: string) {
  const containerRef = useRef<HTMLDivElement>(null);
  const widths = useSyncExternalStore(
    subscribe,
    () => snapshot(contestId),
    serverSnapshot,
  );

  const commit = useCallback(
    (next: Widths) => {
      try {
        window.localStorage.setItem(storageKey(contestId), JSON.stringify(next));
      } catch {
        // A browser refusing storage costs the participant nothing this
        // session: the drag has already happened, and the container still
        // carries the width the pointer left it at.
      }
      cache.set(contestId, { raw: JSON.stringify(next), value: next });
      for (const listener of listeners) listener();
    },
    [contestId],
  );

  return { containerRef, widths, commit };
}

/**
 * One draggable edge.
 *
 * `role="separator"` with `aria-valuenow` and arrow keys, because a divider
 * that can only be moved with a mouse is a divider half the room cannot move
 * — and this one decides how much of the screen the questions get.
 */
export function PaneHandle({
  label,
  property,
  rem,
  direction,
  containerRef,
  onResize,
  className,
}: {
  /** The accessible name, in the participant's language. */
  label: string;
  /** The custom property this handle drives — never derived from the label, which is translated. */
  property: "--pane-schema" | "--pane-side";
  rem: number;
  /** Which way the pointer moves to make this pane wider. */
  direction: 1 | -1;
  containerRef: React.RefObject<HTMLDivElement | null>;
  onResize: (rem: number) => void;
  className?: string;
}) {
  const dragging = useRef<{ startX: number; startRem: number } | null>(null);

  // Read once, when the drag starts, not on every pointer event.
  // `getComputedStyle` is a synchronous style read, and reading on the next
  // event flushes the write from the previous one — a forced recalculation of
  // a grid whose middle column may hold a thousand-row table, per pointer
  // event. The root font size cannot change mid-drag.
  const remRef = useRef(16);

  return (
    <div
      role="separator"
      aria-label={label}
      aria-orientation="vertical"
      aria-valuenow={Math.round(rem)}
      aria-valuemin={MIN_REM}
      aria-valuemax={MAX_REM}
      tabIndex={0}
      className={cn(
        // A hairline that widens to a grab area without taking layout space:
        // the column it separates is the thing, not the handle.
        "relative w-px shrink-0 cursor-col-resize bg-line",
        "after:absolute after:inset-y-0 after:-left-1 after:-right-1 after:content-['']",
        "hover:bg-line-2 focus-visible:bg-accent focus-visible:outline-none",
        className,
      )}
      onKeyDown={(event) => {
        const step = event.shiftKey ? 4 : 1;
        if (event.key === "ArrowLeft") {
          event.preventDefault();
          onResize(clamp(rem - step * direction));
        } else if (event.key === "ArrowRight") {
          event.preventDefault();
          onResize(clamp(rem + step * direction));
        }
      }}
      onPointerDown={(event) => {
        event.currentTarget.setPointerCapture(event.pointerId);
        remRef.current = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
        dragging.current = { startX: event.clientX, startRem: rem };
      }}
      onPointerMove={(event) => {
        const drag = dragging.current;
        const container = containerRef.current;
        if (!drag || !container) return;

        const moved = ((event.clientX - drag.startX) / remRef.current) * direction;
        const next = clamp(drag.startRem + moved);
        // Straight onto the DOM: see this file's own doc for why this does not
        // go through state until the pointer is released.
        container.style.setProperty(property, `${next}rem`);
      }}
      onPointerUp={(event) => {
        const drag = dragging.current;
        const container = containerRef.current;
        dragging.current = null;
        if (!drag || !container) return;

        const moved = ((event.clientX - drag.startX) / remRef.current) * direction;
        onResize(clamp(drag.startRem + moved));
      }}
    />
  );
}

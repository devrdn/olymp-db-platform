"use client";

import * as React from "react";
import { CircleQuestionMark } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * A "?" beside a label holding a longer explanation; short rules stay on screen
 * in `Field`'s `hint`.
 *
 * Hover, a press and keyboard focus each open it, tracked separately so one
 * closing does not undo another; a press pins it, and a first press never
 * closes what hover or focus opened (touch browsers focus on tap). Escape, a
 * press outside and focus leaving close it. A transparent bridge and a short
 * grace period keep it open while the pointer crosses to the bubble.
 *
 * The bubble is positioned against the trigger, not portalled to `<body>`,
 * because the dialog that hosts one closes on any press outside its subtree.
 * Its offset is measured on open, resize and scroll so it stays inside the
 * viewport. Hand-written to avoid a positioning library on `/play`.
 *
 * Follows the WAI-ARIA tooltip pattern; the `role="tooltip"` element is always
 * rendered (hidden while closed) so `aria-describedby` resolves. Callers: keep
 * it out of a `<label>` or heading, which would absorb its name, and inside a
 * `<fieldset disabled>` put it in the `<legend>`, the only part not disabled.
 */

/** Gap between the bubble and the viewport edge, in CSS pixels. */
const EDGE = 8;

/**
 * How long a hover-opened bubble survives the pointer leaving, so a pointer
 * cutting the corner reaches it.
 */
const LEAVE_GRACE_MS = 200;

/** Any one of these keeps the bubble open. */
type Openers = { hover: boolean; focus: boolean; pinned: boolean };

const CLOSED: Openers = { hover: false, focus: false, pinned: false };

export function Tooltip({
  label,
  children,
  className,
  id,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
  /** Set by `Field` so its control is described by the explanation too. */
  id?: string;
}) {
  const generatedId = React.useId();
  const bubbleId = id ?? generatedId;
  const [openers, setOpeners] = React.useState<Openers>(CLOSED);
  const open = openers.hover || openers.focus || openers.pinned;

  const rootRef = React.useRef<HTMLSpanElement>(null);
  const bubbleRef = React.useRef<HTMLSpanElement>(null);
  const leaveTimer = React.useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const cancelLeave = React.useCallback(() => {
    clearTimeout(leaveTimer.current);
    leaveTimer.current = undefined;
  }, []);

  const close = React.useCallback(() => {
    cancelLeave();
    setOpeners(CLOSED);
  }, [cancelLeave]);

  // A pending grace timer must not fire into an unmounted component.
  React.useEffect(() => cancelLeave, [cancelLeave]);

  // Capture phase: Escape closes only this bubble, not the modal around it, and
  // a control that stops propagation cannot keep it open.
  React.useEffect(() => {
    if (!open) return;

    const onKeyDown = (event: KeyboardEvent) => {
      // Escape while an input method is composing belongs to the composition.
      if (event.key !== "Escape" || event.isComposing) return;
      event.stopPropagation();
      close();
    };
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current?.contains(event.target as Node)) return;
      close();
    };

    window.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => {
      window.removeEventListener("keydown", onKeyDown, true);
      document.removeEventListener("pointerdown", onPointerDown, true);
    };
  }, [open, close]);

  // Measured before paint and written to the element directly: it depends on
  // layout, and state would cost a render per scroll event.
  const place = React.useCallback(() => {
    const root = rootRef.current;
    const bubble = bubbleRef.current;
    if (!root || !bubble) return;

    // `clientWidth`, not `innerWidth`: a classic scrollbar is not room.
    const viewport = document.documentElement;
    const room = viewport.clientWidth - 2 * EDGE;

    bubble.style.maxWidth = "";
    if (bubble.offsetWidth > room) bubble.style.maxWidth = `${room}px`;

    const anchor = root.getBoundingClientRect();
    const left = Math.max(EDGE, Math.min(anchor.left, viewport.clientWidth - EDGE - bubble.offsetWidth));
    bubble.style.left = `${left - anchor.left}px`;

    const below = viewport.clientHeight - anchor.bottom;
    const fitsBelow = below >= bubble.offsetHeight + 2 * EDGE;
    bubble.dataset.side = !fitsBelow && anchor.top > below ? "top" : "bottom";
  }, []);

  React.useLayoutEffect(() => {
    if (!open) return;
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, { capture: true, passive: true });
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, { capture: true });
    };
  }, [open, place]);

  // A finger fires pointerenter too, even one only scrolling the form; touch
  // opens through the click instead.
  const onPointerEnter = (event: React.PointerEvent) => {
    if (event.pointerType === "touch") return;
    cancelLeave();
    setOpeners((current) => ({ ...current, hover: true }));
  };
  const onPointerLeave = (event: React.PointerEvent) => {
    if (event.pointerType === "touch") return;
    cancelLeave();
    leaveTimer.current = setTimeout(() => {
      leaveTimer.current = undefined;
      setOpeners((current) => ({ ...current, hover: false }));
    }, LEAVE_GRACE_MS);
  };

  return (
    <span
      ref={rootRef}
      data-slot="tooltip"
      className={cn("relative inline-flex", className)}
      onPointerEnter={onPointerEnter}
      onPointerLeave={onPointerLeave}
      onBlur={(event) => {
        // Focus moving to the bubble (a press inside it) is not leaving.
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) close();
      }}
    >
      <button
        type="button"
        aria-describedby={bubbleId}
        onFocus={() => setOpeners((current) => ({ ...current, focus: true }))}
        onClick={() => {
          cancelLeave();
          // Only a press on a pinned bubble closes it, so a press never undoes
          // what hover or focus just opened.
          setOpeners((current) => (current.pinned ? CLOSED : { ...current, pinned: true }));
        }}
        className={cn(
          // 24px is the WCAG 2.5.8 minimum target; the negative margin keeps
          // the row height.
          "-my-1 inline-flex size-6 shrink-0 items-center justify-center rounded-full",
          "text-ink-3 transition-colors duration-(--t-input) ease-standard hover:text-ink",
          open && "text-ink",
        )}
      >
        <CircleQuestionMark aria-hidden="true" className="size-4" />
        <span className="sr-only">{label}</span>
      </button>

      <span
        ref={bubbleRef}
        id={bubbleId}
        role="tooltip"
        hidden={!open}
        // Focusable by a press only, so a press inside moves focus here rather
        // than to `<body>`, which the blur check reads as staying.
        tabIndex={-1}
        onPointerDown={() => setOpeners((current) => ({ ...current, pinned: true }))}
        className={cn(
          "absolute top-full left-0 z-50 mt-1.5 w-max max-w-tip",
          "border border-line-2 bg-panel px-3 py-2 text-left font-sans text-small text-ink-2 normal-case",
          "cursor-auto select-text",
          // Opacity only, so reduced motion needs no replacement.
          "transition-opacity duration-(--t-input) ease-standard starting:opacity-0",
          "data-[side=top]:top-auto data-[side=top]:bottom-full data-[side=top]:mt-0 data-[side=top]:mb-1.5",
          // Invisible bridge over the gap, so the pointer crossing it stays
          // inside.
          "before:absolute before:inset-x-0 before:-top-1.5 before:h-1.5",
          "data-[side=top]:before:top-auto data-[side=top]:before:-bottom-1.5",
        )}
      >
        {children}
      </span>
    </span>
  );
}

"use client";

import * as React from "react";
import { CircleQuestionMark } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * A "?" beside a label, and the explanation it holds.
 *
 * The form screens used to print every explanation permanently under its
 * field, which made a long form read like a manual. The client asked for the
 * explanations to move behind a question mark and for the short rules — a
 * format, a limit, what an empty field means — to stay on screen, because
 * those are what a person needs *before* they get something wrong. So this
 * holds only the first kind; `Field`'s `hint` slot still holds the second.
 *
 * It opens on every way a person can reach it, not only on hover: an
 * olympiad hall has tablets, and a hover-only "?" on a tablet is a picture of
 * a question mark. Hover, a press (a tap is a press), and keyboard focus each
 * open it; Escape, a press anywhere outside it, and focus moving away close
 * it. Each opener is tracked on its own, so the pointer leaving does not close
 * a bubble the keyboard opened, and a press "pins" it — a first press never
 * closes what hover or focus already opened, which is what keeps a tap from
 * opening and closing in the same gesture on a browser that focuses a button
 * when it is touched.
 *
 * It has to be readable, not merely present: the longest explanation is a few
 * hundred characters. The bubble is a measure wide (`--container-tip`), not a
 * strip across half the screen. The bubble sits inside the same element as
 * the trigger, so moving the pointer from the "?" onto the text never counts
 * as leaving; a transparent bridge covers the gap between the two, and a short
 * grace period covers a diagonal path that cuts a corner. Pressing inside the
 * bubble pins it, so selecting the text does not make it vanish mid-drag.
 *
 * It never leaves the viewport. The bubble is positioned against the trigger
 * rather than portalled to `<body>`, because the one dialog that carries a
 * "?" is a Base UI modal that treats a press outside its own subtree as a
 * request to close; what moves is the offset, measured on open and on every
 * resize or scroll, so the bubble slides left at the right edge, narrows on a
 * phone, and flips above the trigger when there is no room below.
 *
 * Accessibility follows the WAI-ARIA tooltip pattern: a real
 * `<button type="button">` whose name comes from the locale dictionary (the
 * glyph is decorative and names nothing), `aria-describedby` pointing at a
 * `role="tooltip"` element that is always in the DOM — hidden with the
 * `hidden` attribute while closed, so the description resolves even before
 * anything has been opened, and so the text is in the server-rendered HTML.
 *
 * Hand-written rather than Base UI's `Tooltip` or `Popover`, for the reason
 * `tabs.tsx` records: the rewrite of `Button` and `Input` without Base UI took
 * 37% off the gzipped `/play` route, and a floating-ui positioning engine is a
 * lot to bring back for one bubble that only ever needs to stay inside the
 * viewport.
 *
 * Two placement rules for callers. Keep it out of a `<label>` and out of a
 * heading: the button's name would become part of the name of the thing
 * beside it ("Points Hint"). And inside a `<fieldset disabled>`, put it in the
 * fieldset's `<legend>` — the one place the HTML spec exempts from the
 * fieldset's disabling — or the explanation of a frozen setting becomes
 * unreadable exactly when the setting is frozen.
 */

/** Room left between the bubble and either edge of the viewport, in CSS pixels. */
const EDGE = 8;

/**
 * How long a bubble opened by hovering survives the pointer leaving it.
 *
 * Long enough for a pointer that cuts the corner between the "?" and the
 * bubble to arrive before it closes, short enough that a pointer which really
 * has moved on does not leave text hanging over the next field.
 */
const LEAVE_GRACE_MS = 200;

/** Why the bubble is open. Any one of them keeps it open. */
type Openers = { hover: boolean; focus: boolean; pinned: boolean };

const CLOSED: Openers = { hover: false, focus: false, pinned: false };

export function Tooltip({
  label,
  children,
  className,
}: {
  /** The trigger's accessible name, from the dictionary (`chrome.helpLabel`). */
  label: string;
  /** The explanation itself. */
  children: React.ReactNode;
  className?: string;
}) {
  const bubbleId = React.useId();
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

  // Dismissal, only while open. Escape is taken on the window in the capture
  // phase so it closes this bubble and nothing else: the "?" in the title
  // editor sits inside a modal dialog that closes on the same key, and one
  // press should close one layer. A press outside is taken in the capture
  // phase too, so a control that stops propagation cannot keep the bubble
  // open over it.
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

  // Placement, measured before paint so the bubble never flashes at a
  // position it is about to leave. Written to the element directly rather
  // than through state: it is a function of the layout, which React does not
  // own, and a state round trip would be a second render per scroll event.
  const place = React.useCallback(() => {
    const root = rootRef.current;
    const bubble = bubbleRef.current;
    if (!root || !bubble) return;

    // `clientWidth` rather than `innerWidth`: a classic scrollbar is not room.
    const viewport = document.documentElement;
    const room = viewport.clientWidth - 2 * EDGE;

    // Narrower than the measure only when the screen itself is.
    bubble.style.maxWidth = "";
    if (bubble.offsetWidth > room) bubble.style.maxWidth = `${room}px`;

    // Start under the trigger and slide left as far as the right edge needs,
    // never past the left edge.
    const anchor = root.getBoundingClientRect();
    const left = Math.max(EDGE, Math.min(anchor.left, viewport.clientWidth - EDGE - bubble.offsetWidth));
    bubble.style.left = `${left - anchor.left}px`;

    // Below by default; above only when below is too short and above is not.
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

  // Hover belongs to devices that have it. A finger fires pointerenter too —
  // including a finger that only landed on the "?" to scroll the form — and
  // treating that as hover would flash the bubble at everybody scrolling a
  // settings page on a tablet. A tap opens it through the click below.
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
          // A press pins. Only a press on an already pinned bubble closes it,
          // so a press never undoes what hover or focus just opened.
          setOpeners((current) => (current.pinned ? CLOSED : { ...current, pinned: true }));
        }}
        className={cn(
          // 24px is the smallest target WCAG 2.5.8 accepts for a finger; the
          // negative margin keeps it from making the label row taller.
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
        // Focusable by a press only, never by Tab: a press inside the bubble
        // then moves focus into it rather than to `<body>`, which the blur
        // check above reads as staying.
        tabIndex={-1}
        onPointerDown={() => setOpeners((current) => ({ ...current, pinned: true }))}
        className={cn(
          "absolute top-full left-0 z-50 mt-1.5 w-max max-w-tip",
          "border border-line-2 bg-panel px-3 py-2 text-left font-sans text-small text-ink-2 normal-case",
          "cursor-auto select-text",
          // Arriving is a reaction to a hover or a press; opacity only, so
          // reduced motion has nothing to replace (SPEC §6).
          "transition-opacity duration-(--t-input) ease-standard starting:opacity-0",
          "data-[side=top]:top-auto data-[side=top]:bottom-full data-[side=top]:mt-0 data-[side=top]:mb-1.5",
          // The bridge: an invisible strip over the gap between the trigger
          // and the bubble, so the pointer crossing it is still inside.
          "before:absolute before:inset-x-0 before:-top-1.5 before:h-1.5",
          "data-[side=top]:before:top-auto data-[side=top]:before:-bottom-1.5",
        )}
      >
        {children}
      </span>
    </span>
  );
}

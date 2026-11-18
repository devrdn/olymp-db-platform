"use client";

import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";

import { Menu, X } from "lucide-react";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

import { BAR_TRACKS } from "./bar-tracks";

/**
 * The bar's burger: a disclosure on the browser's Popover API, not a modal.
 * The browser opens and closes it with no script (before hydration too), and
 * closes it on Escape and on a tap outside; this component adds the label and
 * `aria-expanded`, closing on navigation and on a resize that hides it, and
 * putting focus back on the button. The page behind stays live, so nothing
 * traps focus. The contents are rendered by the server.
 */
export function MenuButton({
  labels,
  className,
  children,
}: {
  labels: { open: string; close: string };
  className?: string;
  children: React.ReactNode;
}) {
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  // Mirrors `open` for the handlers, so a close is never sent to a closed panel.
  const shown = useRef(false);

  const close = useCallback(() => {
    if (shown.current) panel.current?.hidePopover();
  }, []);

  useEffect(() => {
    const element = panel.current;
    if (!element) return;
    const onToggle = (event: Event) => {
      shown.current = (event as ToggleEvent).newState === "open";
      setOpen(shown.current);
    };
    element.addEventListener("toggle", onToggle);
    return () => element.removeEventListener("toggle", onToggle);
  }, []);

  // Before paint, so the new page never shows under the panel.
  const pathname = usePathname();
  const shownAt = useRef(pathname);
  useLayoutEffect(() => {
    if (shownAt.current === pathname) return;
    shownAt.current = pathname;
    close();
  }, [pathname, close]);

  useEffect(() => {
    if (!open) return;
    // The browser closes the panel on Escape and on a tap outside as well; doing
    // it here too returns focus to the button where a tap never focused it.
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      close();
      button.current?.focus();
    };
    const onPointerDown = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) close();
    };
    // Past its breakpoint the button is hidden, and an open panel would stay up with it.
    const onResize = () => {
      if (root.current && getComputedStyle(root.current).display === "none") close();
    };
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("resize", onResize);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("resize", onResize);
    };
  }, [open, close]);

  return (
    <div
      ref={root}
      data-slot="app-bar-menu"
      className={cn("flex shrink-0", className)}
      onBlur={(event) => {
        // Only a known element outside: a tap on the panel's blank space blurs to nothing.
        const next = event.relatedTarget;
        if (next && !event.currentTarget.contains(next)) close();
      }}
    >
      <button
        ref={button}
        type="button"
        popoverTarget={id}
        aria-expanded={open}
        aria-controls={id}
        aria-label={open ? labels.close : labels.open}
        title={open ? labels.close : labels.open}
        className="grid size-11 place-items-center rounded-full text-ink-2 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
      >
        {open ? (
          <X className="size-5" strokeWidth={1.75} aria-hidden />
        ) : (
          <Menu className="size-5" strokeWidth={1.75} aria-hidden />
        )}
      </button>
      {/* In the top layer, so placed against the viewport: the sticky bar is
         always the first 48px and a rule. The panel's box covers the rest of
         the screen but lets every pointer through, so a tap on the page or the
         hatched fields is a tap outside. */}
      <div
        ref={panel}
        id={id}
        popover="auto"
        className={cn(
          BAR_TRACKS,
          "pointer-events-none fixed inset-x-0 top-12 bottom-0 m-0 mt-px h-auto w-auto max-w-none grid-rows-1 overflow-visible border-0 bg-transparent p-0 text-ink open:grid",
        )}
      >
        <div
          // A tap on a link closes it, and on the page already shown keeps focus
          // on the button rather than drop it from a hidden link to the body.
          onClick={(event) => {
            const link = (event.target as Element).closest("a");
            if (!link) return;
            close();
            if (new URL(link.href, window.location.href).pathname === pathname) button.current?.focus();
          }}
          // A pixel wider on each side, so its edges lie on the hatched fields' rules.
          className="pointer-events-auto col-start-2 -mx-px max-h-full self-start overflow-y-auto overscroll-contain border-x border-b border-line bg-bg px-10 py-2 max-narrow:mx-0 max-narrow:border-x-0 max-narrow:px-4.5"
        >
          {children}
        </div>
      </div>
    </div>
  );
}

"use client";

import { useCallback, useEffect, useId, useLayoutEffect, useRef, useSyncExternalStore } from "react";

import { Menu, X } from "lucide-react";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

import { BAR_TRACKS } from "./bar-tracks";

/** Safari before 17 and Firefox before 125, both still supported, have no Popover API. */
function hasPopover() {
  return "popover" in HTMLElement.prototype;
}

/**
 * The panel's real state, whether or not a script saw it open. A browser
 * without the API throws on `:popover-open`, as on any pseudo-class it does not
 * know, so there only the attribute is read.
 */
function isShown(panel: HTMLElement | null) {
  if (!panel) return false;
  return hasPopover() ? panel.matches(":popover-open") : panel.hasAttribute("open");
}

/**
 * The bar's burger: a disclosure on the browser's Popover API, not a modal.
 * Where the API exists the browser opens and closes it with no script (before
 * hydration too), and closes it on Escape and on a tap outside. Where it does
 * not, the script toggles an `open` attribute instead, which the same `open:`
 * styles read. This component adds the label and `aria-expanded`, closing on
 * navigation and on a resize that hides it, and putting focus back on the
 * button. The page behind stays live, so nothing traps focus. The contents are
 * rendered by the server.
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

  // Read from the element, so a panel opened before hydration is seen as open.
  const subscribe = useCallback((onChange: () => void) => {
    const element = panel.current;
    element?.addEventListener("toggle", onChange);
    return () => element?.removeEventListener("toggle", onChange);
  }, []);
  const open = useSyncExternalStore(subscribe, () => isShown(panel.current), () => false);

  const show = useCallback((next: boolean) => {
    const element = panel.current;
    if (!element || isShown(element) === next) return;
    if (hasPopover()) {
      if (next) element.showPopover();
      else element.hidePopover();
      return;
    }
    element.toggleAttribute("open", next);
    element.dispatchEvent(new Event("toggle"));
  }, []);
  const close = useCallback(() => show(false), [show]);

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
    // Only the bar lies outside the panel: the dim under it is the panel's own.
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (!panel.current?.contains(target) && !button.current?.contains(target)) close();
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
        // Where the browser has no popover, the press is handled here.
        onClick={() => {
          if (!hasPopover()) show(!isShown(panel.current));
        }}
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
      {/* Placed against the viewport, in the top layer or, without the API,
         as a fixed box over the page: the sticky bar is always the first 48px
         and a rule. `not-open:hidden` hides it where the browser has no popover
         rules of its own; an unknown `:popover-open` inside its `:is()` is
         dropped, not fatal. */}
      <div
        ref={panel}
        id={id}
        popover="auto"
        className={cn(
          BAR_TRACKS,
          "fixed inset-x-0 top-12 bottom-0 z-10 m-0 mt-px h-auto w-auto max-w-none grid-rows-1 overflow-visible border-0 bg-transparent p-0 text-ink not-open:hidden open:grid",
        )}
      >
        {/* Dims the page as a dialog's backdrop does. It is part of the panel,
           so a tap on it is not a tap outside: nothing closes until the tap's
           click, which therefore lands here and never on the page beneath. A
           button, so the browser closes the panel on it with no script. */}
        <button
          type="button"
          tabIndex={-1}
          aria-hidden
          data-slot="app-bar-menu-scrim"
          popoverTarget={id}
          popoverTargetAction="hide"
          onClick={() => {
            close();
            button.current?.focus();
          }}
          className="absolute inset-0 cursor-default bg-ink/45"
        />
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
          className="relative col-start-2 -mx-px max-h-full self-start overflow-y-auto overscroll-contain border-x border-b border-line bg-bg px-10 py-2 max-narrow:mx-0 max-narrow:border-x-0 max-narrow:px-4.5"
        >
          {children}
        </div>
      </div>
    </div>
  );
}

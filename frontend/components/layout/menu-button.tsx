"use client";

import { useEffect, useId, useRef, useState } from "react";

import { Menu, X } from "lucide-react";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

/**
 * The bar's burger: a disclosure, not a modal. The panel holds links and the
 * bar's own controls, the page behind stays live, and a tap outside simply
 * closes it, so there is nothing to trap focus in. The panel is positioned by
 * the nearest positioned ancestor, the bar's column, and its contents are
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
  const [open, setOpen] = useState(false);
  const close = () => setOpen(false);

  // Navigating closes it, decided during render so the new page never paints under it.
  const pathname = usePathname();
  const [shownAt, setShownAt] = useState(pathname);
  if (shownAt !== pathname) {
    setShownAt(pathname);
    setOpen(false);
  }

  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setOpen(false);
      button.current?.focus();
    };
    const onPointerDown = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [open]);

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
        aria-expanded={open}
        aria-controls={id}
        aria-label={open ? labels.close : labels.open}
        title={open ? labels.close : labels.open}
        onClick={() => setOpen((value) => !value)}
        className="grid size-11 place-items-center rounded-full text-ink-2 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
      >
        {open ? (
          <X className="size-5" strokeWidth={1.75} aria-hidden />
        ) : (
          <Menu className="size-5" strokeWidth={1.75} aria-hidden />
        )}
      </button>
      <div
        id={id}
        hidden={!open}
        // A tap on a link closes it even when the address stays the same.
        onClick={(event) => {
          if ((event.target as Element).closest("a")) close();
        }}
        // A pixel wider on each side, so its edges lie on the hatched fields' rules.
        className="absolute -inset-x-px top-full mt-px max-h-[calc(100dvh-(--spacing(12)))] overflow-y-auto overscroll-contain border-x border-b border-line bg-bg px-10 py-2 max-narrow:inset-x-0 max-narrow:border-x-0 max-narrow:px-4.5"
      >
        {children}
      </div>
    </div>
  );
}

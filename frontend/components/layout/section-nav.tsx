"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

import type { Destination } from "./destinations";

/**
 * Destinations in the bar, for any signed-in role: a row from 1024px up, and
 * full-width rows in the menu below that. Only what the account may open,
 * decided by the layout from the permissions the API reports; a link that
 * answers 403 discloses the screen. A client component only to mark the
 * current destination.
 */
export function SectionNav({ items, layout = "row" }: { items: Destination[]; layout?: "row" | "menu" }) {
  const pathname = usePathname();

  return (
    <nav className={layout === "menu" ? "flex flex-col" : "flex min-w-0 items-center gap-1"}>
      {items.map((item) => {
        // Stays marked anywhere inside the section.
        const current = pathname === item.href || pathname.startsWith(`${item.href}/`);

        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={current ? "page" : undefined}
            // Chromium leaves a partly visible focus target where it is, so a link
            // cut off by the menu's scroller is scrolled fully in. Default
            // behaviour, so a reduced-motion preference is not overridden.
            onFocus={(event) => event.currentTarget.scrollIntoView({ block: "nearest", inline: "nearest" })}
            className={cn(
              "text-control transition-colors duration-(--t-input) ease-standard",
              // A menu row wraps a long label rather than cut it: it has the full width.
              layout === "menu" ? "flex min-h-12 items-center py-2" : "truncate px-2 py-1",
              current ? "text-ink" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {item.label}
          </Link>
        );
      })}
    </nav>
  );
}

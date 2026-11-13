"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

/**
 * Destinations in the bar, for any signed-in role. Only what the account may
 * open, decided by the layout from the permissions the API reports; a link that
 * answers 403 discloses the screen. A client component only to mark the current
 * destination.
 */
export function SectionNav({ items }: { items: { href: string; label: string }[] }) {
  const pathname = usePathname();

  return (
    <nav className="flex min-w-0 items-center gap-1 max-narrow:min-w-max">
      {items.map((item) => {
        // Stays marked anywhere inside the section.
        const current = pathname === item.href || pathname.startsWith(`${item.href}/`);

        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={current ? "page" : undefined}
            // Chromium leaves a partly visible focus target where it is, so a link
            // cut off by the narrow scroller's edge is scrolled fully in. Default
            // behaviour, so a reduced-motion preference is not overridden.
            onFocus={(event) => event.currentTarget.scrollIntoView({ block: "nearest", inline: "nearest" })}
            className={cn(
              "truncate px-2 py-1 text-control max-narrow:flex max-narrow:min-h-11 max-narrow:items-center transition-colors duration-(--t-input) ease-standard",
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

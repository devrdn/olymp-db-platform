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
    <nav className="flex min-w-0 items-center gap-1">
      {items.map((item) => {
        // Stays marked anywhere inside the section.
        const current = pathname === item.href || pathname.startsWith(`${item.href}/`);

        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={current ? "page" : undefined}
            className={cn(
              "truncate px-2 py-1 text-control transition-colors duration-(--t-input) ease-standard",
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

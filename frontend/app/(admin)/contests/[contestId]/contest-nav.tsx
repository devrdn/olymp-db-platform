"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { scrollCue } from "@/components/ui/scroll-cue";
import { cn } from "@/lib/utils";

export type NavItem = {
  href: string;
  label: string;
  /** Match exactly: the overview's address is a prefix of every other section's. */
  exact?: boolean;
  /** What this section still owes, shown beside it. */
  note?: string;
};

export type NavGroup = { label?: string; items: NavItem[] };

/**
 * The workspace sections, as a column from `wide` up so each can carry a note;
 * below it a row above the work, which then takes the whole width. A client
 * component only to know the pathname.
 * `aria-current="page"` carries what the border shows.
 */
export function ContestNav({
  groups,
  label,
}: {
  groups: NavGroup[];
  /** The list's accessible name, from the dictionary. */
  label: string;
}) {
  const pathname = usePathname();

  return (
    <nav
      ref={scrollCue}
      aria-label={label}
      className={cn(
        "flex flex-col gap-6",
        // Below the breakpoint the column becomes one scrolling row, and only
        // the row wears the cue: the column never scrolls, and a mask would
        // clip its focus ring.
        "max-wide:flex-row max-wide:gap-5 max-wide:overflow-x-auto max-wide:border-b max-wide:border-line max-wide:pb-0 max-wide:scroll-cue",
      )}
    >
      {groups.map((group, index) => (
        <div key={group.label ?? index} className="flex flex-col gap-1.5 max-wide:contents">
          {group.label ? (
            <span className="px-2 pb-1 font-mono text-label text-ink-3 uppercase max-wide:hidden">
              {group.label}
            </span>
          ) : null}

          {group.items.map((item) => {
            const current = item.exact
              ? pathname === item.href
              : pathname === item.href || pathname.startsWith(`${item.href}/`);

            return (
              <Link
                key={item.href}
                href={item.href}
                aria-current={current ? "page" : undefined}
                className={cn(
                  "group flex items-baseline justify-between gap-3 rounded-none px-2 py-1.5 text-control",
                  "border-l-2 transition-colors duration-(--t-input) ease-standard",
                  // As a row, the mark moves to the bottom edge.
                  "max-wide:shrink-0 max-wide:border-l-0 max-wide:border-b-2 max-wide:-mb-px max-wide:px-0 max-wide:py-3 max-wide:whitespace-nowrap",
                  current
                    ? "border-ink text-ink"
                    : "border-transparent text-ink-3 hover:border-line-2 hover:text-ink",
                )}
              >
                <span>{item.label}</span>

                {item.note ? (
                  <span className="shrink-0 font-mono text-label text-warn max-wide:hidden">
                    {item.note}
                  </span>
                ) : null}
              </Link>
            );
          })}
        </div>
      ))}
    </nav>
  );
}

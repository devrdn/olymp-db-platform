"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

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
 * The workspace sections, as a column so each can carry a note (a row already
 * overflowed at 375px). A client component only to know the pathname.
 * `aria-current="page"` carries what the border shows.
 */
export function ContestNav({ groups }: { groups: NavGroup[] }) {
  const pathname = usePathname();

  return (
    <nav
      aria-label="Contest sections"
      className={cn(
        "flex flex-col gap-6",
        // Below the breakpoint the column becomes one scrolling row.
        "max-narrow:flex-row max-narrow:gap-5 max-narrow:overflow-x-auto max-narrow:border-b max-narrow:border-line max-narrow:pb-0",
      )}
    >
      {groups.map((group, index) => (
        <div key={group.label ?? index} className="flex flex-col gap-1.5 max-narrow:contents">
          {group.label ? (
            <span className="px-2 pb-1 font-mono text-label text-ink-3 uppercase max-narrow:hidden">
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
                  "max-narrow:shrink-0 max-narrow:border-l-0 max-narrow:border-b-2 max-narrow:-mb-px max-narrow:px-0 max-narrow:py-3 max-narrow:whitespace-nowrap",
                  current
                    ? "border-ink text-ink"
                    : "border-transparent text-ink-3 hover:border-line-2 hover:text-ink",
                )}
              >
                <span>{item.label}</span>

                {item.note ? (
                  <span className="shrink-0 font-mono text-label text-warn max-narrow:hidden">
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

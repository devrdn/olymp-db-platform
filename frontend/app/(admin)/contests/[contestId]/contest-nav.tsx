"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

export type NavItem = {
  href: string;
  label: string;
  /**
   * Match this address exactly rather than by prefix.
   *
   * The overview owns the workspace's bare address, which is a prefix of every
   * other section's. Left to prefix matching it would be marked current on
   * every item at once, which is the same as marking none.
   */
  exact?: boolean;
  /**
   * What this section still owes, in a word or two.
   *
   * The reason the navigation is a column and not a row. "1 without text"
   * belongs beside "Questions", where it is a piece of work with an address,
   * not four screens away in a gate report an author has to go and read.
   */
  note?: string;
};

export type NavGroup = { label?: string; items: NavItem[] };

/**
 * The workspace's sections.
 *
 * A column, not a row of tabs. Five sections today and the architecture names
 * two more admin screens for the same contest — the query log and the reports
 * — and a horizontal row was already scrolling sideways at 375px with the five.
 * A column also has somewhere to put a note per section, which a row does not.
 *
 * It is not a panel. There is no fill and no border box: the column is held by
 * one hairline, which is the same device the sign-in screen is built from and
 * the same one the specification means by "panels are separated by a rule".
 *
 * A Client Component for one reason — a layout cannot see the pathname, and
 * the current section has to be known to be marked. Everything under it stays
 * a Server Component.
 *
 * `aria-current="page"` carries what the mark says. A section distinguished
 * only by a border is undistinguished for anybody not looking at it.
 */
export function ContestNav({ groups }: { groups: NavGroup[] }) {
  const pathname = usePathname();

  return (
    <nav
      aria-label="Contest sections"
      className={cn(
        "flex flex-col gap-6",
        // Below the layout breakpoint the column has nowhere to stand, so it
        // becomes a single scrolling row — the same links in the same order,
        // which is what the mobile reset asks of every asymmetric grid.
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
                  // On a narrow screen the mark moves from the left edge to the
                  // bottom one, because the column has become a row.
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

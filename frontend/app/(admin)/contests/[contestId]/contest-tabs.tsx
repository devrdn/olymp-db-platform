"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

export type Tab = {
  href: string;
  label: string;
  /**
   * Match this address exactly rather than by prefix.
   *
   * The overview owns the workspace's bare address, which is a prefix of every
   * other section's. Left to prefix matching it would be marked current on all
   * five tabs at once.
   */
  exact?: boolean;
};

/**
 * The workspace's sections.
 *
 * A row of links under a rule, not a card of buttons: the underline marks the
 * current section against the same hairline every band is built from, so the
 * navigation is drawn with the system's own structure rather than dropped on
 * top of it.
 *
 * A Client Component for one reason — a layout cannot see the pathname, and
 * the current section has to be known to be marked. That is the whole of its
 * interactivity; everything under it stays a Server Component.
 *
 * `aria-current="page"` carries what the underline says. A section marked only
 * by a border is unmarked for anybody not looking at it.
 */
export function ContestTabs({ tabs }: { tabs: Tab[] }) {
  const pathname = usePathname();

  // No rule of its own. The band this sits at the bottom of already draws one,
  // and that one runs the full width of the page through the hatched margins,
  // which a rule inside the content column cannot. The links reach down onto
  // it with `-mb-px`, so the current section's 2px mark lands on the structure
  // instead of beside a second copy of it.
  return (
    <nav className="flex gap-6 overflow-x-auto">
      {tabs.map((tab) => {
        const current = tab.exact
          ? pathname === tab.href
          : pathname === tab.href || pathname.startsWith(`${tab.href}/`);

        return (
          <Link
            key={tab.href}
            href={tab.href}
            aria-current={current ? "page" : undefined}
            className={cn(
              "-mb-px shrink-0 border-b-2 py-3 text-control whitespace-nowrap",
              "transition-colors duration-(--t-input) ease-standard",
              current
                ? "border-ink text-ink"
                : "border-transparent text-ink-3 hover:border-line-2 hover:text-ink",
            )}
          >
            {tab.label}
          </Link>
        );
      })}
    </nav>
  );
}

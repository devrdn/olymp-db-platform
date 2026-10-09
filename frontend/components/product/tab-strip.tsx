import Link from "next/link";

import { cn } from "@/lib/utils";

export type Tab = {
  href: string;
  label: string;
};

/**
 * Linkable tabs: the address, not state, holds the tab, so it can be shared and
 * reloaded and the server reads only that tab's data. `aria-current="page"`
 * marks the current one. The strip scrolls sideways inside itself so the page
 * never does at 375px. The caller passes the current href, since each screen
 * knows its own `?tab=` default.
 */
export function TabStrip({
  label,
  tabs,
  current,
}: {
  /** Names the group for a screen reader. */
  label: string;
  tabs: readonly Tab[];
  current: string;
}) {
  return (
    <nav
      aria-label={label}
      className="flex min-w-0 gap-6 overflow-x-auto border-b border-line max-narrow:gap-5"
    >
      {tabs.map((tab) => (
        <Link
          key={tab.href}
          href={tab.href}
          scroll={false}
          aria-current={tab.href === current ? "page" : undefined}
          className={cn(
            "-mb-px shrink-0 border-b-2 py-3 text-control whitespace-nowrap transition-colors duration-(--t-input) ease-standard",
            tab.href === current
              ? "border-ink text-ink"
              : "border-transparent text-ink-3 hover:border-line-2 hover:text-ink",
          )}
        >
          {tab.label}
        </Link>
      ))}
    </nav>
  );
}

import Link from "next/link";

import { cn } from "@/lib/utils";

/** One tab: where it leads and what it is called. */
export type Tab = {
  href: string;
  label: string;
};

/**
 * A strip of linkable tabs, as the monitoring page and a participant's own
 * report both wear it.
 *
 * Links rather than buttons, and the address rather than state: a tab can be
 * kept, shared and reloaded, the server reads that tab's data and only that
 * tab's before the page arrives, and the back button does what a reader
 * expects. `aria-current="page"` marks the one in the address, which is what
 * a screen reader announces — the underline alone says nothing.
 *
 * The strip scrolls sideways inside itself. Five labels do not fit a phone's
 * width, and the rule the whole product holds at 375 px is that the page
 * never scrolls sideways; something on it may.
 *
 * Which tab is current is decided by the caller and passed as the href it
 * matches, because each screen reads its own `?tab=` and knows its own
 * default.
 */
export function TabStrip({
  label,
  tabs,
  current,
}: {
  /** Names the group for a screen reader — "What this participant did". */
  label: string;
  tabs: readonly Tab[];
  /** The href of the tab in the address. */
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

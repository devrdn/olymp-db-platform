import Link from "next/link";
import { ChevronRight } from "lucide-react";

/**
 * The trail from the register to the current page: an ordered list in a named
 * `nav`, with the last step marked rather than linked.
 */
export type Crumb = {
  /** Absent on the last step. */
  href?: string;
  label: string;
};

export function Breadcrumbs({ items, label }: { items: Crumb[]; label: string }) {
  return (
    <nav aria-label={label} className="min-w-0">
      <ol className="flex min-w-0 flex-wrap items-center gap-1 font-mono text-data text-ink-3">
        {items.map((item, index) => (
          <li key={item.href ?? item.label} className="flex min-w-0 items-center gap-1">
            {/* Decorative; the list already carries the order. */}
            {index > 0 ? (
              <ChevronRight aria-hidden className="size-3 shrink-0 text-line-2" strokeWidth={2} />
            ) : null}

            {item.href ? (
              <Link
                href={item.href}
                className="truncate transition-colors duration-(--t-input) ease-standard hover:text-ink"
              >
                {item.label}
              </Link>
            ) : (
              <span aria-current="page" className="truncate text-ink-2">
                {item.label}
              </span>
            )}
          </li>
        ))}
      </ol>
    </nav>
  );
}

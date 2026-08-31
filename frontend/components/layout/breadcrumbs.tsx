import Link from "next/link";
import { ChevronRight } from "lucide-react";

/**
 * Where the visitor is, and every step back out.
 *
 * The contest workspace is three levels deep — the register, one contest, one
 * of its sections — and until now the only way out was a single "back to the
 * register" link, which says where one step leads but not where you are. The
 * trail says both, which is the whole difference between a back button and a
 * breadcrumb.
 *
 * An ordered list inside a named `nav`, because that is what assistive
 * technology looks for: `<ol>` gives the steps their number and order, the
 * name distinguishes this navigation from the bar's, and the last item is
 * marked rather than linked — a link to the page you are standing on is a
 * control that does nothing.
 */
export type Crumb = {
  /** Absent on the last step: that is where the visitor already is. */
  href?: string;
  label: string;
};

export function Breadcrumbs({ items, label }: { items: Crumb[]; label: string }) {
  return (
    <nav aria-label={label} className="min-w-0">
      <ol className="flex min-w-0 flex-wrap items-center gap-1 font-mono text-data text-ink-3">
        {items.map((item, index) => (
          <li key={item.href ?? item.label} className="flex min-w-0 items-center gap-1">
            {/* Decorative: read aloud, a trail of three would announce two
                chevrons nobody asked about. The list already carries the
                order. */}
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

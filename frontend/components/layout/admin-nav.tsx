"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

/**
 * Where an administrator can go, in the bar.
 *
 * Only what the account may actually open: a link that answers 403 teaches
 * somebody that a screen exists and that they are not welcome on it, which is
 * both rude and a small disclosure. The layout decides from the permissions
 * the API reports, so a role added as data changes what is offered without a
 * change here.
 *
 * A client component for one reason: the current destination has to be marked,
 * and only the browser knows which one that is. Everything it renders was
 * decided on the server.
 *
 * Words, with no glyph beside them. Two destinations are told apart by reading
 * them, so an icon here adds a mark to look past rather than a shape to aim
 * at — and the design system's first rule is that the interface around the
 * data is rules and typography (SPEC section 2).
 */
export function AdminNav({ items }: { items: { href: string; label: string }[] }) {
  const pathname = usePathname();

  return (
    <nav className="flex min-w-0 items-center gap-1">
      {items.map((item) => {
        // A section stays marked while inside it: /contests/<id>/story is
        // still contests, and a mark that disappears one level in would leave
        // the reader with no idea where they are.
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

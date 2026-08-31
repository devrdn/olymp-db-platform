"use client";

import { ClipboardList, ScrollText } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { cn } from "@/lib/utils";

/**
 * The icons a destination may carry, named by what they lead to rather than
 * by what they look like.
 *
 * A closed set, not a component passed in: the layout that builds these items
 * is a Server Component, and a React element crossing that boundary would
 * have to be serialisable. A key is, and it keeps the drawing here where the
 * sizes and stroke weights already agree with the rest of the bar.
 */
const ICONS = {
  contests: ClipboardList,
  audit: ScrollText,
} as const;

export type NavIcon = keyof typeof ICONS;

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
 * The icon sits beside the word rather than replacing it. Two destinations
 * need no glyph to be told apart, and a bar of unlabelled pictograms is a
 * quiz; what the icon buys is a shape to aim at, which is what the eye
 * actually returns to. It is `aria-hidden` for the same reason — announced, it
 * would make every destination read twice.
 */
export function AdminNav({
  items,
}: {
  items: { href: string; label: string; icon?: NavIcon }[];
}) {
  const pathname = usePathname();

  return (
    <nav className="flex min-w-0 items-center gap-1">
      {items.map((item) => {
        // A section stays marked while inside it: /contests/<id>/story is
        // still contests, and a mark that disappears one level in would leave
        // the reader with no idea where they are.
        const current = pathname === item.href || pathname.startsWith(`${item.href}/`);
        const Icon = item.icon ? ICONS[item.icon] : null;

        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={current ? "page" : undefined}
            className={cn(
              "flex items-center gap-1.5 rounded-full px-2 py-1 text-control transition-colors duration-(--t-input) ease-standard",
              current ? "text-ink" : "text-ink-3 hover:text-ink-2",
            )}
          >
            {Icon ? <Icon className="size-4 shrink-0" strokeWidth={1.75} aria-hidden /> : null}
            <span className="truncate">{item.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}

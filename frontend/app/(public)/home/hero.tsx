import { ArrowRight } from "lucide-react";
import Link from "next/link";

import { Band } from "@/components/layout/band";
import { OrnamentStar } from "@/components/product/ornament";
import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The top of the front page: what this installation is called, what it is
 * for, and the two doors out of the sentence.
 *
 * Both actions move with the visitor, and neither is decoration:
 *
 * - The main one is sign-in for somebody without a session and "my contests"
 *   for somebody with one. Returning a signed-in person to the showcase is
 *   fine; handing them the form they have already filled in is not.
 * - The second one points at contests, and *which* contests depends on who is
 *   asking. The catalogue at `/open` is behind sign-in, so a visitor without a
 *   session would be shown the sign-in form where they asked for a list. They
 *   get the list further down this same page instead.
 *
 * The name is a prop rather than a read: it comes from the installation's
 * settings, and the page above decides what to show when there is none. That
 * keeps this component renderable in a test without a running API.
 */
export function Hero({
  name,
  signedIn,
  dict,
}: {
  /** What this installation calls itself, already resolved to something. */
  name: string;
  signedIn: boolean;
  dict: Dictionary;
}) {
  const t = dict.home.hero;

  return (
    <Band className="gap-8">
      {/* The star sits inside the heading rather than beside it, so it is
          measured in the heading's own em: a mark at the size of a letter,
          which stays that size through every step of the display clamp. */}
      <h1 className="max-w-head text-display text-balance text-ink">
        <OrnamentStar className="mr-[0.22em]" />
        {name}
      </h1>

      <p className="max-w-lede text-lede text-ink-2">{dict.home.lede}</p>

      <div className="flex flex-wrap items-center gap-x-7 gap-y-4">
        <Link
          href={signedIn ? "/my" : "/login"}
          className={cn(buttonVariants({ variant: "primary", size: "lg" }))}
        >
          {signedIn ? t.mine : t.signIn}
        </Link>

        {/* Text and an arrow, not a second button: two filled actions side by
            side is two primaries, and then neither is one. */}
        <Link
          href={signedIn ? "/open" : "#contests"}
          className="inline-flex items-center gap-1.5 text-control text-ink-2 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:underline"
        >
          {t.browse}
          <ArrowRight className="size-4" strokeWidth={1.75} aria-hidden />
        </Link>
      </div>
    </Band>
  );
}

import { ArrowRight } from "lucide-react";
import Link from "next/link";

import { Band } from "@/components/layout/band";
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
    /* Centred, and the whole band with it.
     *
     * The rest of the product is a left rule and a column of data hanging off
     * it, because that is how a table is read. A front page is not read that
     * way: there is one sentence, and everything on the screen is pointing at
     * it. Centring is what says "this is the whole of it" — and it is why the
     * glow behind it can be a single soft source rather than something that
     * has to follow a column edge.
     */
    <Band className="relative items-center gap-8 py-20 text-center max-narrow:py-14">
      {/* The light behind the title, and the only gradient in the product.
          Decorative, so it is hidden from a reader and pinned behind the
          text; it is a token, so both themes and the contrast checker know
          about it. */}
      <span aria-hidden className="glow" />

      <h1 className="max-w-head text-display text-balance text-ink">{name}</h1>

      <p className="max-w-lede text-lede text-balance text-ink-2">{dict.home.lede}</p>

      <div className="flex flex-wrap items-center justify-center gap-x-7 gap-y-4">
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

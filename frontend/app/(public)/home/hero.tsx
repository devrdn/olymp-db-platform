import { ArrowRight } from "lucide-react";
import Link from "next/link";

import { Band } from "@/components/layout/band";
import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

/**
 * The front page's top: name, purpose and two actions. The main one is sign-in,
 * or "my contests" with a session. The second points at contests: the `/open`
 * catalogue is behind sign-in, so a visitor without a session goes to the list
 * on this page. `name` is a prop so tests need no API.
 */
export function Hero({
  name,
  signedIn,
  dict,
}: {
  /** The installation's name, already resolved. */
  name: string;
  signedIn: boolean;
  dict: Dictionary;
}) {
  const t = dict.home.hero;

  return (
    /*
     * Centred: unlike the data screens, the front page has one sentence, and
     * the glow can be a single source.
     */
    <Band className="relative items-center gap-8 py-20 text-center max-narrow:py-14">
      {/* The product's only gradient; decorative, and a token so both themes and
         the contrast check know it. */}
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

        {/* Text and an arrow, so there is only one primary. */}
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

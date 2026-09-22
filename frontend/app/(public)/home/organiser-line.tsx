import Link from "next/link";

import { Band } from "@/components/layout/band";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * One line for the people who set the questions.
 *
 * A teacher and a participant sign in through the same door — the API has no
 * separate staff endpoint, and where an account lands is decided by the
 * permissions it turns out to hold. What a teacher lacks is not a door but the
 * knowledge that it is theirs too, and a sentence costs less than a second
 * entrance that would have to be kept honest.
 *
 * Deliberately not a section with a heading: it is an aside near the foot of
 * the page, and giving it the weight of "How it works" would suggest the page
 * is addressed to organisers, which it is not.
 */
export function OrganiserLine({ dict }: { dict: Dictionary }) {
  const t = dict.home.organisers;

  return (
    <Band className="flex-row flex-wrap items-baseline gap-x-3 gap-y-2 py-9 max-narrow:py-7">
      <p className="text-body text-ink-2">{t.line}</p>
      <Link
        href="/login"
        className="text-body text-ink underline underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-accent"
      >
        {t.link}
      </Link>
    </Band>
  );
}

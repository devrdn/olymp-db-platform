import Link from "next/link";

import { Band } from "@/components/layout/band";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * One aside telling teachers the same sign-in is theirs too (an account's
 * permissions decide where it lands). Not a headed section: the page is not
 * addressed to organisers.
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

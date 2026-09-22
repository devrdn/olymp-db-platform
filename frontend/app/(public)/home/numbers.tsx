import { Band } from "@/components/layout/band";
import type { PublicStats } from "@/lib/api/showcase";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * What this installation has done, in four numbers.
 *
 * The same pair the profile's summary is made of — a mono figure over a mono
 * caption, in a definition list — and deliberately not a second idiom. A
 * showcase that drew its own tiles would read as an advertisement for the
 * product rather than as part of it, and the reader who follows one of these
 * numbers into the product should meet the same shapes on the other side.
 *
 * The one thing it does that the profile's summary does not is disappear. A
 * failed read there costs a line of explanation, because the reader is signed
 * in, knows what they asked for and can try again. Here the reader has just
 * arrived, has asked for nothing and has no way to tell a broken read from an
 * empty installation: a strip of zeroes would tell them this place has never
 * run anything, which is a claim, and a false one. A showcase with no figures
 * is a showcase; a showcase full of zeroes is a broken system (design §2.3).
 */
export function Numbers({
  stats,
  dict,
}: {
  /** `null` is a failed read, and it costs the whole strip. */
  stats: PublicStats | null;
  dict: Dictionary;
}) {
  if (stats === null) return null;

  const t = dict.home.numbers;

  return (
    <Band>
      {/* Four across, two by two below the layout's one breakpoint: four
          columns on a 375px screen gives every caption four lines of two
          letters. */}
      <dl className="grid grid-cols-4 gap-x-10 gap-y-8 max-narrow:grid-cols-2">
        <Figure label={t.contests} value={stats.contests} />
        <Figure label={t.participants} value={stats.participants} />
        <Figure label={t.queries} value={stats.queries} />
        <Figure label={t.solved} value={stats.solved} />
      </dl>
    </Band>
  );
}

/**
 * One number over its caption.
 *
 * The term comes first in the markup, because that is what a definition list
 * is, and the column is reversed for the eye: the number is read first and the
 * caption explains it. Reversing the markup instead would hand a screen reader
 * a value with no name in front of it.
 *
 * A step larger than the profile's `h3`, and no other difference: this is the
 * one place in the product where these four numbers are the content rather
 * than a header above it. `tabular-nums` is what keeps the four of them on one
 * grid, so a five-figure query count does not shove its caption sideways.
 */
function Figure({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex min-w-0 flex-col-reverse gap-1.5">
      <dt className="font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="font-mono text-h2 text-ink tabular-nums">{value}</dd>
    </div>
  );
}

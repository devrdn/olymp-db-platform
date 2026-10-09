import { Band } from "@/components/layout/band";
import type { PublicStats } from "@/lib/api/showcase";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * Four figures about the installation, in the profile summary's idiom. On a
 * failed read the strip disappears: a newcomer cannot tell failure from an
 * empty installation, and a row of zeroes would be a false claim.
 */
export function Numbers({
  stats,
  dict,
}: {
  /** `null` is a failed read and hides the strip. */
  stats: PublicStats | null;
  dict: Dictionary;
}) {
  if (stats === null) return null;

  const t = dict.home.numbers;

  return (
    <Band>
      {/* Two by two below the breakpoint, or captions wrap letter by letter. */}
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
 * A number over its caption; the term comes first in the markup so screen
 * readers hear the name before the value, reversed visually. `tabular-nums`
 * keeps the four aligned.
 */
function Figure({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex min-w-0 flex-col-reverse gap-1.5">
      <dt className="font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="font-mono text-h2 text-ink tabular-nums">{value}</dd>
    </div>
  );
}

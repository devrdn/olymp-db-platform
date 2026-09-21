import type { ProfileSummary as Numbers } from "@/lib/api/profile";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * What the account has done, in four numbers.
 *
 * A strip of definitions rather than four tiles: the direction has no boxes,
 * and what separates this from the contests under it is a rule (SPEC §5). The
 * figures are mono with tabular figures, so the four of them line up and a
 * three-digit query count does not shove its caption sideways.
 *
 * `null` is a failed read, not an empty one, and it costs one line. The four
 * numbers are the smaller of this page's two reads; the contests below are
 * what somebody came for, and losing the header over a summary would be
 * spending the whole screen on the cheaper half.
 */
export function ProfileSummary({
  summary,
  dict,
}: {
  summary: Numbers | null;
  dict: Dictionary;
}) {
  const t = dict.profile.summary;

  return (
    <section aria-labelledby="profile-summary" className="flex flex-col gap-5 border-t border-line pt-6">
      <h2 id="profile-summary" className="font-mono text-label text-ink-3 uppercase">
        {t.heading}
      </h2>

      {summary === null ? (
        <p role="alert" className="max-w-body text-body text-bad">
          {t.failed}
        </p>
      ) : (
        <dl className="grid grid-cols-4 gap-x-6 gap-y-7 max-narrow:grid-cols-2">
          <Figure label={t.contests} value={summary.contests} />
          <Figure label={t.finished} value={summary.finished} />
          <Figure label={t.queries} value={summary.queries} />
          <Figure label={t.solved} value={summary.solved} />
        </dl>
      )}
    </section>
  );
}

/**
 * One number over its caption.
 *
 * The term comes first in the markup, because that is what a definition list
 * is, and the column is reversed for the eye: the number is what is read
 * first and the caption is what explains it. Reversing the markup instead
 * would hand a screen reader a value with no name in front of it.
 */
function Figure({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex min-w-0 flex-col-reverse gap-1">
      <dt className="font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="font-mono text-h3 text-ink tabular-nums">{value}</dd>
    </div>
  );
}

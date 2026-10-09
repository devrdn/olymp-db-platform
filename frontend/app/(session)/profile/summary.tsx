import type { ProfileSummary as Numbers } from "@/lib/api/profile";
import type { Dictionary } from "@/lib/i18n/dictionary";

/**
 * Four figures about the account, as a definition strip with tabular numerals
 * (SPEC.md §5.2). `null` is a failed read and costs one line, not the page.
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
 * A number over its caption; the term comes first in the markup so screen
 * readers hear the name before the value, reversed visually.
 */
function Figure({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex min-w-0 flex-col-reverse gap-1">
      <dt className="font-mono text-label text-ink-3 uppercase">{label}</dt>
      <dd className="font-mono text-h3 text-ink tabular-nums">{value}</dd>
    </div>
  );
}

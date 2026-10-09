import { formatDay, formatMoment, formatTime, isSameDay } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";

/** Thin space, en dash, thin space. */
const RANGE = " – ";

/**
 * A contest's window: one date and a time range when it fits in a day, so it
 * does not repeat the date. Shared so every screen prints it the same way.
 *
 * Timestamps go through `lib/format/datetime`, which pins the university's
 * zone; otherwise server (UTC) and browser would differ and hydration would
 * mismatch.
 */
export function ContestWindow({
  startsAt,
  endsAt,
  locale,
  unscheduled,
  until,
}: {
  startsAt?: string;
  endsAt?: string;
  locale: Locale;
  /** Shown when no date is set. */
  unscheduled: string;
  /** Joins two different days. */
  until: string;
}) {
  if (!startsAt) return <span className="text-ink-3">{unscheduled}</span>;

  if (!endsAt) return <>{formatMoment(startsAt, { locale })}</>;

  if (isSameDay(startsAt, endsAt, { locale })) {
    return (
      <>
        {formatDay(startsAt, { locale })}
        {/* A constant in an expression: a unicode escape in JSX text is printed literally. */}
        <span className="block text-ink-3">
          {formatTime(startsAt, { locale }) + RANGE + formatTime(endsAt, { locale })}
        </span>
      </>
    );
  }

  return (
    <>
      {formatMoment(startsAt, { locale })}
      <span className="block text-ink-3">
        {until} {formatMoment(endsAt, { locale })}
      </span>
    </>
  );
}

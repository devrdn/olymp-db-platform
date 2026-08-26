import { formatDay, formatMoment, formatTime, isSameDay } from "@/lib/format/datetime";
import type { Locale } from "@/lib/i18n/config";

/** Thin space, en dash, thin space — the typographic form of a time range. */
const RANGE = " – ";

/**
 * A contest's window, in two lines that never repeat themselves.
 *
 * A contest that starts and ends on one day — which is most of them — used to
 * print its date twice, and the second line ran past the column and broke
 * between the hour and the meridiem. One date and a time range says the same
 * thing in half the width and reads down the column, which is what a register
 * column is for.
 *
 * Shared rather than colocated: the author's register and the participant's
 * both print this, and a contest reading "14 May, 10:00–13:00" on one screen
 * and "14 May 10:00 / to 14 May 13:00" on the other is one fact told two ways.
 *
 * Every timestamp goes through `lib/format/datetime`, which pins the zone to
 * the university's. Left to the runtime, the server would format in UTC and
 * the browser in whatever the student's laptop is set to, and React would
 * report the difference as a hydration mismatch.
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
  /** What to say when no date has been set yet. */
  unscheduled: string;
  /** The word joining two different days. */
  until: string;
}) {
  if (!startsAt) return <span className="text-ink-3">{unscheduled}</span>;

  if (!endsAt) return <>{formatMoment(startsAt, { locale })}</>;

  if (isSameDay(startsAt, endsAt, { locale })) {
    return (
      <>
        {formatDay(startsAt, { locale })}
        {/* The separator is a constant read into an expression, never typed
            between the two times as JSX text. JSX text is literal: a unicode
            escape written there is not an escape and reaches the screen as its
            six characters, which is what the author's register was printing. */}
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

/**
 * Interface dates.
 *
 * Two rules the spec is strict about. The timezone is explicit rather than the
 * runtime's, because a Server Component and the browser that hydrates it would
 * otherwise disagree and React would report a mismatch. And this formatter is
 * for interface text only: values inside a query result grid are shown exactly
 * as PostgreSQL returned them, so a participant can match what they see to
 * what they wrote.
 */

/** The installation's timezone. One place to change when a deployment moves. */
export const DEFAULT_TIME_ZONE = "Europe/Chisinau";

type Options = { timeZone?: string; locale?: string };

function resolve({ timeZone = DEFAULT_TIME_ZONE, locale = "ru-RU" }: Options) {
  return { timeZone, locale };
}

export function formatMoment(iso: string, options: Options = {}): string {
  const { timeZone, locale } = resolve(options);

  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone,
  }).format(new Date(iso));
}

/** The date alone, for a value whose time is carried on its own line. */
export function formatDay(iso: string, options: Options = {}): string {
  const { timeZone, locale } = resolve(options);

  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone,
  }).format(new Date(iso));
}

/** The time alone, in the installation's timezone. */
export function formatTime(iso: string, options: Options = {}): string {
  const { timeZone, locale } = resolve(options);

  return new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    timeZone,
  }).format(new Date(iso));
}

/**
 * Whether two moments fall on the same calendar day **where the contest is
 * held**, not where the server or the reader happens to be.
 *
 * This is what lets a register print one date and a time range instead of the
 * same date twice: most contests start and finish inside one day, and the
 * repetition is what pushed the column into wrapping mid-value. Comparing the
 * formatted parts rather than the UTC timestamps is the point — a window from
 * 23:00 to 01:00 is two days, and 01:00 UTC to 05:00 UTC in Chisinau is one.
 */
export function isSameDay(a: string, b: string, options: Options = {}): boolean {
  const { timeZone } = resolve(options);
  const parts = new Intl.DateTimeFormat("en-CA", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    timeZone,
  });

  return parts.format(new Date(a)) === parts.format(new Date(b));
}

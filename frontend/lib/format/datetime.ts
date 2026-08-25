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

export function formatMoment(
  iso: string,
  options: { timeZone?: string; locale?: string } = {},
): string {
  const { timeZone = DEFAULT_TIME_ZONE, locale = "ru-RU" } = options;

  return new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone,
  }).format(new Date(iso));
}

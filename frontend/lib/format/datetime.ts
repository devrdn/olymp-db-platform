/**
 * Interface dates.
 *
 * Three rules, and the first two are about the same failure: a Server
 * Component and the browser that hydrates it must produce the same string,
 * character for character, or React throws the tree away and says so.
 *
 * The timezone is explicit rather than the runtime's — the same instant is a
 * different calendar day in Chisinau and in UTC. And nothing here asks the
 * locale data to *join* anything: the string CLDR uses between a date and a
 * time differs by ICU version, so Node 22 renders `Sep 7, 2026, 11:38 PM`
 * where a newer Chrome renders `Sep 7, 2026 at 11:38 PM`. That was a real
 * mismatch on the participant's query log, and it is not reproducible on a
 * machine whose browser happens to carry Node's ICU — which is most of them,
 * until it is not.
 *
 * The third rule: this formatter is for interface text only. Values inside a
 * query result grid are shown exactly as PostgreSQL returned them, so a
 * participant can match what they see to what they wrote.
 */

/** The installation's timezone. One place to change when a deployment moves. */
export const DEFAULT_TIME_ZONE = "Europe/Chisinau";

type Options = { timeZone?: string; locale?: string };

function resolve({ timeZone = DEFAULT_TIME_ZONE, locale = "ru-RU" }: Options) {
  return { timeZone, locale };
}

/**
 * The formatters, kept.
 *
 * `new Intl.DateTimeFormat(...)` loads and compiles locale data; it is the
 * expensive part of formatting a date, and it was being paid on every single
 * call — twice for `formatMoment`, which composes the two below. Measured:
 * a thousand `formatMoment` calls cost 83.3ms with fresh formatters and
 * 1.6ms with kept ones, which is fifty-two times, or 83µs a call.
 *
 * That is a screen's worth of work on the paths that use it. The admin's
 * database register prints a moment per row, up to five hundred of them, in
 * a Client Component — so the same 42ms is paid once on the server and again
 * during hydration. The participant's query log grows without a ceiling as
 * "load older" is pressed.
 *
 * The key is the two things a formatter's identity actually depends on here
 * — the locale and the zone — plus which of the shapes below it is. Both are
 * closed sets in this product (three languages, the installation's own zone),
 * so nothing here grows without bound: what is cached is a handful of
 * objects for the life of the process, not one per value formatted.
 */
const formatters = new Map<string, Intl.DateTimeFormat>();

function formatter(
  shape: string,
  locale: string,
  timeZone: string,
  build: () => Intl.DateTimeFormat,
): Intl.DateTimeFormat {
  const key = `${shape}\u0000${locale}\u0000${timeZone}`;
  let cached = formatters.get(key);
  if (!cached) {
    cached = build();
    formatters.set(key, cached);
  }
  return cached;
}

export function formatMoment(iso: string, options: Options = {}): string {
  // Composed rather than asked for as one skeleton. A single
  // `Intl.DateTimeFormat` carrying both date and time fields picks the join
  // from the locale data, and that join is exactly what moves between ICU
  // versions (see the file doc). This comma is ours, so every runtime agrees
  // on it.
  return `${formatDay(iso, options)}, ${formatTime(iso, options)}`;
}

/** The date alone, for a value whose time is carried on its own line. */
export function formatDay(iso: string, options: Options = {}): string {
  const { timeZone, locale } = resolve(options);

  return formatter("day", locale, timeZone, () =>
    new Intl.DateTimeFormat(locale, {
      day: "numeric",
      month: "short",
      year: "numeric",
      timeZone,
    }),
  ).format(new Date(iso));
}

/**
 * The time alone, in the installation's timezone, on a 24-hour clock.
 *
 * `h23` in every locale, English included. Two reasons, and the second is why
 * it is here rather than a preference: a competition reads deadlines off this
 * clock and "12:00" must not be ambiguous, and the space some ICU versions put
 * before AM/PM is U+202F where others use an ordinary one — the same
 * hydration trap as the date-time join, one field along.
 */
export function formatTime(iso: string, options: Options = {}): string {
  const { timeZone, locale } = resolve(options);

  return formatter("time", locale, timeZone, () =>
    new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
      timeZone,
    }),
  ).format(new Date(iso));
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
  const parts = formatter("calendar-day", "en-CA", timeZone, () =>
    new Intl.DateTimeFormat("en-CA", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      timeZone,
    }),
  );

  return parts.format(new Date(a)) === parts.format(new Date(b));
}

/**
 * The offset of a zone at a given instant, in milliseconds.
 *
 * There is no API that answers this directly, so the instant is formatted in
 * the zone, read back as though those numbers were UTC, and the difference
 * taken. That difference is the offset.
 */
function zoneOffset(instant: number, timeZone: string): number {
  const parts = Object.fromEntries(
    formatter("zone-offset", "en-US", timeZone, () =>
      new Intl.DateTimeFormat("en-US", {
        timeZone,
        hour12: false,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      }),
    )
      .formatToParts(new Date(instant))
      .map((part) => [part.type, part.value]),
  );

  const asIfUTC = Date.UTC(
    Number(parts.year),
    Number(parts.month) - 1,
    Number(parts.day),
    // A midnight formatted as "24" by some engines would otherwise land a day out.
    Number(parts.hour) % 24,
    Number(parts.minute),
    Number(parts.second),
  );

  return asIfUTC - instant;
}

/**
 * A wall clock typed into a `datetime-local` field, as the instant it names.
 *
 * The browser hands back "2026-05-14T10:00" with no zone at all, and the API
 * takes RFC 3339. `new Date()` on that string resolves it in whatever zone the
 * process happens to run in — UTC inside the container, the developer's zone
 * on a laptop — so a contest set to start at ten would start at ten somewhere
 * nobody involved lives. The zone is therefore named, and it is the same one
 * every timestamp in the interface is already formatted in.
 *
 * The offset is applied twice because it is itself a function of the instant:
 * a time entered on the far side of a clock change is corrected by the old
 * offset first, which lands close enough for the second pass to be exact.
 */
export function instantFromWallClock(wall: string, options: Options = {}): string | null {
  const { timeZone } = resolve(options);

  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(wall.trim());
  if (!match) return null;

  const [, year, month, day, hour, minute, second = "0"] = match;
  const naive = Date.UTC(
    Number(year),
    Number(month) - 1,
    Number(day),
    Number(hour),
    Number(minute),
    Number(second),
  );
  if (Number.isNaN(naive)) return null;

  let instant = naive - zoneOffset(naive, timeZone);
  instant = naive - zoneOffset(instant, timeZone);

  return new Date(instant).toISOString();
}

/**
 * The other direction: an instant as the wall clock a `datetime-local` field
 * shows. Its value must be exactly "YYYY-MM-DDTHH:mm" — anything else and the
 * browser silently renders an empty field, which reads as "no date set" on a
 * contest that has one.
 */
export function wallClockFromInstant(iso: string, options: Options = {}): string {
  const { timeZone } = resolve(options);

  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";

  const parts = Object.fromEntries(
    formatter("wall-clock", "en-CA", timeZone, () =>
      new Intl.DateTimeFormat("en-CA", {
        timeZone,
        hour12: false,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      }),
    )
      .formatToParts(at)
      .map((part) => [part.type, part.value]),
  );

  const hour = String(Number(parts.hour) % 24).padStart(2, "0");
  return `${parts.year}-${parts.month}-${parts.day}T${hour}:${parts.minute}`;
}

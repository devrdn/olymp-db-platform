/**
 * Interface dates. The server and the hydrating browser must produce the same
 * string, so the timezone is explicit and nothing asks the locale data to join
 * date and time: that joiner differs between ICU versions (Node's ", " against
 * a newer Chrome's " at "), which caused hydration mismatches.
 *
 * For interface text only: query result cells are shown as PostgreSQL
 * returned them.
 */

/** The installation's timezone. */
export const DEFAULT_TIME_ZONE = "Europe/Chisinau";

type Options = { timeZone?: string; locale?: string };

function resolve({ timeZone = DEFAULT_TIME_ZONE, locale = "ru-RU" }: Options) {
  return { timeZone, locale };
}

/**
 * Cached formatters. Building an `Intl.DateTimeFormat` is the expensive part:
 * measured at about 50 times the cost of reusing one, on registers that format
 * hundreds of rows. Keyed by shape, locale and zone, all closed sets, so the
 * cache stays a handful of objects.
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
  // Composed with our own comma: a combined skeleton takes the joiner from ICU.
  return `${formatDay(iso, options)}, ${formatTime(iso, options)}`;
}

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
 * `h23` in every locale: deadlines must not be ambiguous, and ICU versions
 * disagree on the space before AM/PM, another hydration mismatch.
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

/** `formatTime` with seconds, for log rows a few seconds apart. */
export function formatSeconds(iso: string, options: Options = {}): string {
  const { timeZone, locale } = resolve(options);

  return formatter("seconds", locale, timeZone, () =>
    new Intl.DateTimeFormat(locale, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
      timeZone,
    }),
  ).format(new Date(iso));
}

/**
 * Whether two moments fall on the same calendar day in the installation's
 * zone, not in UTC or the reader's zone.
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
 * A zone's offset at an instant, in milliseconds: the instant formatted in the
 * zone, read back as if UTC, minus the instant. No API gives it directly.
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
 * A `datetime-local` value (no zone) as an RFC 3339 instant in the
 * installation's zone; `new Date()` would use the process's zone instead.
 * The offset is applied twice because it depends on the instant: across a
 * clock change the first pass lands close enough for the second to be exact.
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
 * An instant as a `datetime-local` value. It must be exactly
 * "YYYY-MM-DDTHH:mm", or the browser silently shows an empty field.
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

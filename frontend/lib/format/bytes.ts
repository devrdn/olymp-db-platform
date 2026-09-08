/**
 * A byte count and a duration, rounded to something a person reads while an
 * upload is moving.
 *
 * The one place either is written. `game-databases.tsx` used to carry a
 * byte-for-byte copy of `readableBytes` under its own name, with its own tests
 * over the same five values — two columns of sizes on the two halves of one
 * feature, which would have disagreed the first time anybody changed the
 * rounding, with both sets of tests staying green.
 *
 * Neither is localised: "MiB" and "GiB" are unit abbreviations, not prose, and
 * read the same in every declared language. What a screen wraps around them
 * (the "{rate}/s" template, the dictionary's own `/с` for Russian) is where
 * translation belongs instead.
 */

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"];

/** A byte count, scaled to the unit a person reads — "4.0 MiB", not "4194304". */
export function readableBytes(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${BYTE_UNITS[unit]}`;
}

/**
 * A duration, as a clock reads it — "2:07", or "1:02:07" once there is an
 * hour to show. No unit words, so nothing here needs translating: the shape
 * itself is what a stopwatch or a download manager already reads as time.
 */
export function readableDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.round(totalSeconds));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = seconds % 60;

  const ss = String(rest).padStart(2, "0");
  if (hours > 0) return `${hours}:${String(minutes).padStart(2, "0")}:${ss}`;
  return `${minutes}:${ss}`;
}

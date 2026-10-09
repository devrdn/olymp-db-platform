/**
 * Byte counts and durations for people. Not localised: unit abbreviations read
 * the same in every language; the surrounding template is translated instead.
 */

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"];

/** "4.0 MiB", not "4194304". */
export function readableBytes(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${BYTE_UNITS[unit]}`;
}

/** "2:07", or "1:02:07" once there is an hour. */
export function readableDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.round(totalSeconds));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = seconds % 60;

  const ss = String(rest).padStart(2, "0");
  if (hours > 0) return `${hours}:${String(minutes).padStart(2, "0")}:${ss}`;
  return `${minutes}:${ss}`;
}

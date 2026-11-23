/**
 * A text made safe for one server log line: control characters, and the line
 * and paragraph separators a log viewer also breaks on, become spaces, so a
 * value taken from a request cannot start a forged line; the length is bounded.
 */
export function printable(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ").slice(0, 500);
}

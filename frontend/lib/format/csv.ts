/**
 * CSV export of a query result, for a spreadsheet or another tool. CSV has no
 * null, so `null` is written as an empty field, as `psql`'s `\copy ... csv`
 * does; the table on screen keeps the distinction.
 */

/** RFC 4180 quoting: only fields containing a comma, quote or line break. */
function quoteIfNeeded(value: string): string {
  if (/[",\r\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
}

function isNumericLiteral(value: string): boolean {
  const trimmed = value.trim();
  return trimmed !== "" && Number.isFinite(Number(trimmed));
}

/**
 * Prefixes an apostrophe to a field a spreadsheet would evaluate as a formula:
 * one starting `=`, `@`, a tab or a carriage return, or `+`/`-` when not a
 * number. Negative numbers are left alone so the column still sums and sorts.
 */
function neutralizeFormula(value: string): string {
  const first = value.charAt(0);
  if (first === "=" || first === "@" || first === "\t" || first === "\r") {
    return `'${value}`;
  }
  if ((first === "+" || first === "-") && !isNumericLiteral(value)) {
    return `'${value}`;
  }
  return value;
}

/**
 * One CSV document from the result already in the browser, nothing
 * re-fetched. `\r\n` line endings per RFC 4180. Column names are neutralised
 * too, since an alias can start with a formula character.
 */
export function toCsv(
  columns: readonly string[],
  rows: readonly (readonly (string | null)[])[],
): string {
  const lines = [columns.map(neutralizeFormula).map(quoteIfNeeded).join(",")];
  for (const row of rows) {
    lines.push(row.map((cell) => quoteIfNeeded(neutralizeFormula(cell ?? ""))).join(","));
  }
  return lines.join("\r\n") + "\r\n";
}

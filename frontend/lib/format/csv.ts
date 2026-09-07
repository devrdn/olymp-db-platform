/**
 * The one export format the play workspace offers for a query's result:
 * CSV — what a spreadsheet opens without a dialogue, and what almost every
 * other tool reads too. Chosen over TSV or a JSON array of rows for exactly
 * that reason: this download exists for "open it in a spreadsheet or feed it
 * to another tool" (the plan's own words), and CSV is the one shape both of
 * those already expect.
 *
 * `null` and the empty string are different things in SQL — the console
 * table itself keeps them apart (console.tsx's own `t.null`) — but CSV has no
 * third state for a cell: a `null` is written here as an empty field, the
 * same convention `psql`'s own `\copy ... csv` uses. A reader who needs the
 * distinction back has the table on screen, which never collapses it.
 */

/** RFC 4180's own quoting rule: quote a field that contains the delimiter, a
 * quote, or a line break, and double any quote inside it. Anything else is
 * written bare — the common case, and the one that keeps a plain "id" column
 * readable without a wrapper. */
function quoteIfNeeded(value: string): string {
  if (/[",\r\n]/.test(value)) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
}

/**
 * A field a spreadsheet reads as a formula rather than as text: Excel,
 * LibreOffice and Sheets all treat a cell beginning `=`, `+`, `-` or `@` as
 * one to evaluate, not to display (finding 8). Nothing about this download is
 * an attacker's payload — it is a student's own query result, opened by that
 * same student — but a column of negative numbers or an aggregate named
 * `-total` costs nothing to make inert, and this is meant to be opened in
 * Excel. A leading apostrophe is the ordinary way to say "this is text" to
 * every one of those programs; it is not itself written into the value a
 * plain text reader sees, only into what a spreadsheet displays.
 */
function neutralizeFormula(value: string): string {
  return /^[=+\-@]/.test(value) ? `'${value}` : value;
}

/**
 * Builds one CSV document from a query's own columns and rows — exactly what
 * is already in the browser, nothing re-fetched. The result is already the
 * runner's own truncated answer (queryResultSchema's own `truncated`), so
 * this can never carry more than the table on screen already showed.
 *
 * `\r\n` line endings: the format's own default (RFC 4180 §2.1), and what
 * keeps a spreadsheet that sniffs line endings from mis-reading the file.
 *
 * Column names go through `neutralizeFormula` the same as cells: an aliased
 * column can begin with any of those four characters just as easily as a
 * value can, and the header row is opened in the same spreadsheet.
 */
export function toCsv(columns: readonly string[], rows: readonly (string | null)[][]): string {
  const lines = [columns.map(neutralizeFormula).map(quoteIfNeeded).join(",")];
  for (const row of rows) {
    lines.push(row.map((cell) => quoteIfNeeded(neutralizeFormula(cell ?? ""))).join(","));
  }
  return lines.join("\r\n") + "\r\n";
}

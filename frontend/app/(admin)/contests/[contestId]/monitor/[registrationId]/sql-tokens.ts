/**
 * SQL read into the pieces a read-only view colours: keywords, function
 * names, strings, numbers and comments, and everything else as it is.
 *
 * Not the editor's highlighter. That is CodeMirror, ~140 KiB gzipped and
 * built for typing (components/product/code-editor-core.ts); an organiser
 * reading a list of fifty statements needs colours, not an editor, so this is
 * a single linear pass with no dependency. The colours are the same tokens
 * the editor uses (`--accent-ink`, `--sql-function`, `--sql-string`,
 * `--sql-number`, `--ink-3`), so a statement reads the same on both screens.
 *
 * Lossless by construction: the pieces joined are the text, whatever it is —
 * a string or comment left open runs to the end rather than failing.
 */

export type SqlTokenKind = "keyword" | "function" | "string" | "number" | "comment" | "plain";
export type SqlToken = { kind: SqlTokenKind; text: string };

/** The words coloured as keywords: the statements and clauses a contest's queries are made of. */
const KEYWORDS = new Set(
  (
    "all and any as asc between by case cast create cross current_date current_timestamp delete desc distinct drop " +
    "else end except exists explain false filter from full group having ilike in inner insert intersect interval " +
    "into is join lateral left like limit natural not null nulls offset on or order outer over partition recursive " +
    "returning right rows select set similar table then true union update using values view when where window with"
  ).split(" "),
);

const WORD = /[A-Za-z_][A-Za-z0-9_$]*/y;
const NUMBER = /\d+(?:\.\d*)?(?:[eE][+-]?\d+)?|\.\d+(?:[eE][+-]?\d+)?/y;
const DOLLAR_TAG = /\$[A-Za-z_]*\$/y;

function matchAt(pattern: RegExp, text: string, at: number): string | null {
  pattern.lastIndex = at;
  const match = pattern.exec(text);
  return match ? match[0] : null;
}

/** The end of a single-quoted string starting at `at`, doubled quotes included. */
function stringEnd(sql: string, at: number): number {
  let i = at + 1;
  while (i < sql.length) {
    if (sql[i] === "'") {
      if (sql[i + 1] === "'") {
        i += 2;
        continue;
      }
      return i + 1;
    }
    i += 1;
  }
  return sql.length;
}

export function tokenizeSql(sql: string): SqlToken[] {
  const tokens: SqlToken[] = [];
  let plainFrom = 0;
  let i = 0;

  const emit = (kind: SqlTokenKind, end: number) => {
    if (plainFrom < i) tokens.push({ kind: "plain", text: sql.slice(plainFrom, i) });
    tokens.push({ kind, text: sql.slice(i, end) });
    i = end;
    plainFrom = end;
  };

  while (i < sql.length) {
    const c = sql[i];
    if (c === "-" && sql[i + 1] === "-") {
      const eol = sql.indexOf("\n", i);
      emit("comment", eol < 0 ? sql.length : eol);
    } else if (c === "/" && sql[i + 1] === "*") {
      const close = sql.indexOf("*/", i + 2);
      emit("comment", close < 0 ? sql.length : close + 2);
    } else if (c === "'") {
      emit("string", stringEnd(sql, i));
    } else if (c === '"') {
      // A quoted identifier is a name, not a string: left plain.
      const close = sql.indexOf('"', i + 1);
      i = close < 0 ? sql.length : close + 1;
    } else if (c === "$" && matchAt(DOLLAR_TAG, sql, i)) {
      const tag = matchAt(DOLLAR_TAG, sql, i) as string;
      const close = sql.indexOf(tag, i + tag.length);
      emit("string", close < 0 ? sql.length : close + tag.length);
    } else if (/[A-Za-z_]/.test(c)) {
      const word = matchAt(WORD, sql, i) as string;
      const end = i + word.length;
      if (KEYWORDS.has(word.toLowerCase())) {
        emit("keyword", end);
      } else if (/^\s*\(/.test(sql.slice(end, end + 64))) {
        emit("function", end);
      } else {
        i = end;
      }
    } else if (/[\d.]/.test(c) && matchAt(NUMBER, sql, i)) {
      emit("number", i + (matchAt(NUMBER, sql, i) as string).length);
    } else {
      i += 1;
    }
  }
  if (plainFrom < sql.length) tokens.push({ kind: "plain", text: sql.slice(plainFrom) });
  return tokens;
}

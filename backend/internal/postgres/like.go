package postgres

import "strings"

// escapeLike makes a search string literal inside a LIKE or ILIKE pattern
// (CLAUDE.md rule 3). An unescaped '%' matches every row and a trailing
// backslash fails the statement. Backslash is Postgres's default escape
// character, so the queries need no ESCAPE clause.
//
// NUL bytes and invalid UTF-8 are dropped first: PostgreSQL would fail the
// statement on them (see storableText), and no stored row can contain them.
func escapeLike(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

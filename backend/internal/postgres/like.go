package postgres

import "strings"

// escapeLike makes a search string literal inside a LIKE or ILIKE pattern.
//
// Every free-text filter in this package — an account search, a contest title,
// a participant's name — is typed by a person and lands in a pattern. The
// value travels as a parameter, so it cannot become SQL; what it can do is
// change what the pattern means. An unescaped '%' matches every row, '_'
// matches any single character, and a trailing backslash is a malformed
// pattern that Postgres surfaces as a 500. None of those is what somebody
// searching for "50%" or "under_score" asked for, so the metacharacters are
// neutralised here, and every repository that builds a pattern goes through
// this one function rather than remembering to.
//
// Postgres reads a backslash as the default escape character, which is why
// this does not spell out ESCAPE in the queries.
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

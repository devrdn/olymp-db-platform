package sqlpolicy

import "fmt"

// Code names why a query was refused. Codes are a stable contract: the HTTP
// layer maps each to a sentence per locale and the journal groups by them.
type Code string

const (
	CodeInvalidPolicy         Code = "invalid_policy"
	CodeModeNotSupported      Code = "mode_not_supported"
	CodeParseError            Code = "parse_error"
	CodeNotOneStatement       Code = "not_one_statement"
	CodeStatementNotSupported Code = "statement_not_supported"
	CodeConstructNotSupported Code = "construct_not_supported"
	CodeFunctionNotSupported  Code = "function_not_supported"
	CodeCatalogNotReadable    Code = "catalog_not_readable"
	CodeCatalogNotAllowed     Code = "catalog_not_allowed"
	CodeTooDeep               Code = "too_deep"
	CodeTooLong               Code = "too_long"
	CodeTableNotWritable      Code = "table_not_writable"
	CodeNotPermitted          Code = "not_permitted"

	// CodeArgumentNotBounded refuses a value or row generator whose size is a
	// constant above the limit (repeat('x', 900000000)). A non-constant size
	// is left to the game cluster's per-process memory limit.
	CodeArgumentNotBounded Code = "argument_not_bounded"
)

// Refusal is why one query was not allowed to run.
type Refusal struct {
	Code Code
	// Subject is the offending function, table or construct, in the parser's
	// vocabulary.
	Subject string
	// Position is a 1-based character offset into the query, as PostgreSQL
	// reports it. Only a parse error sets it; zero means no position.
	Position int
}

func (r *Refusal) Error() string {
	if r.Subject == "" {
		return string(r.Code)
	}
	return fmt.Sprintf("%s: %s", r.Code, r.Subject)
}

// Statement is what the checker learned about a query it allowed: only what
// a caller could not tell without parsing again (a prefix match is defeated
// by a leading comment).
type Statement struct {
	// Text is the statement as the parser delimited it, without the trailing
	// semicolon or what followed. Wrap this, not the raw query:
	// `SELECT 1; -- note` is a syntax error inside a FROM (CLAUDE.md rule 14).
	Text string
	// Explain reports an EXPLAIN, which cannot be wrapped in a subquery.
	Explain bool
	// Writes reports a statement that changes the database; only these pay
	// for the disk quota check.
	Writes bool
	// Frees reports a write that can only shrink the database (TRUNCATE, DROP
	// of the participant's own object), so a participant at the quota can
	// still make room. DELETE does not count: its pages stay allocated.
	// Never true without Writes.
	Frees bool
}

// Codes lists every refusal this package can produce, so tests can check
// that the HTTP layer has a sentence for each.
func Codes() []Code {
	return []Code{
		CodeInvalidPolicy, CodeModeNotSupported, CodeParseError,
		CodeNotOneStatement, CodeStatementNotSupported, CodeConstructNotSupported,
		CodeFunctionNotSupported, CodeCatalogNotReadable, CodeCatalogNotAllowed,
		CodeTooDeep, CodeTooLong, CodeTableNotWritable, CodeNotPermitted,
		CodeArgumentNotBounded,
	}
}

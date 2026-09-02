package sqlpolicy

import "fmt"

// Code names why a query was refused.
//
// Stable, because it is the contract: the HTTP layer maps a code to a sentence
// in each locale, and the journal panel groups refusals by it. The Subject
// alongside carries the offending name — a function, a table, a construct — in
// the parser's own vocabulary, which is what an operator needs in the log.
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
)

// Refusal is why one query was not allowed to run.
type Refusal struct {
	Code    Code
	Subject string
}

func (r *Refusal) Error() string {
	if r.Subject == "" {
		return string(r.Code)
	}
	return fmt.Sprintf("%s: %s", r.Code, r.Subject)
}

// Statement is what the checker learned about a query it allowed.
//
// Small on purpose: it carries only what a caller cannot work out again
// without parsing the query a second time, and the caller that needs it — the
// Query Runner — would otherwise be reduced to matching a prefix, which a
// leading comment defeats.
type Statement struct {
	// Text is the statement itself, as the parser delimited it: without the
	// trailing semicolon, and without whatever followed it. A caller that
	// wraps the query in a subquery needs exactly this — `SELECT 1; -- note`
	// is one statement to the parser and a syntax error inside a FROM, and
	// no amount of trimming the original string can tell where it ended.
	Text string
	// Explain reports an EXPLAIN. It matters because an EXPLAIN cannot be
	// placed inside a subquery, so it is the one shape that must not be
	// wrapped when a result is limited.
	Explain bool
	// Writes reports a statement that changes the database. The disk quota is
	// checked before one of these and not before a read, because a read cannot
	// fill a disk and the check costs a round trip.
	Writes bool
}

// Codes lists every refusal this package can produce.
//
// Enumerable so that the layer above can be checked against it rather than
// trusted to keep up: the HTTP layer turns each of these into a sentence in
// three languages, and a code added here without one there would reach a
// participant as whatever the interface says when it does not recognise an
// answer. A list that can be walked is the difference between that being a
// test failure and a discovery during a contest.
func Codes() []Code {
	return []Code{
		CodeInvalidPolicy, CodeModeNotSupported, CodeParseError,
		CodeNotOneStatement, CodeStatementNotSupported, CodeConstructNotSupported,
		CodeFunctionNotSupported, CodeCatalogNotReadable, CodeCatalogNotAllowed,
		CodeTooDeep, CodeTooLong, CodeTableNotWritable, CodeNotPermitted,
	}
}

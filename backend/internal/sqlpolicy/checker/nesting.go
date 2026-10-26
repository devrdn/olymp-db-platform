package checker

import (
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// The parser this package calls is PostgreSQL's own, linked through cgo, and
// it builds its tree by recursion on the C stack. A long enough chain of
// binary operators ("1+1+1+…") overflows that stack before any Go code runs;
// a C stack overflow is a SIGSEGV the Go runtime cannot recover, and it ends
// the Query Runner, the one process that executes every participant's SQL.
// The byte cap does not prevent it: measured, the overflow comes at about
// 25,000 chained operators, which is 50 KiB, inside the 64 KiB limit.
//
// So the text is scanned before the parser sees it, and a chain or a bracket
// nesting no real query approaches is refused as too deep. This is a coarse
// gate in front of the parser, not the depth check: once the tree exists,
// walkNode still refuses real nesting beyond maxDepth. Both bounds sit far
// above anything a person types (hundreds of operators is a generated query)
// and far below the overflow, so no query the parser would otherwise accept
// is refused here.
const (
	maxBracketDepth = 500
	maxOperators    = 4000
)

// guardNesting refuses text whose bracket nesting or operator chain could
// overflow the parser's stack. It skips what the parser never builds a tree
// from: string and dollar-quoted constants, quoted identifiers, and both
// kinds of comment, so a long constant is never mistaken for a long
// expression.
func guardNesting(sql string) error {
	bracket := 0
	operators := 0
	// Whether the previous byte was an operator character, so that a
	// multi-character operator ("<=", "||") counts once: it is one node.
	inOperator := false

	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			i = skipLineComment(sql, i+2)
		case c == '/' && i+1 < n && sql[i+1] == '*':
			i = skipBlockComment(sql, i+2)
		case c == '\'' || c == '"':
			i = skipQuoted(sql, i+1, c)
		case c == '$':
			i = skipDollarQuoted(sql, i)
		case c == '(' || c == '[':
			bracket++
			if bracket > maxBracketDepth {
				return &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooDeep, Subject: fmt.Sprintf("more than %d nested groups", maxBracketDepth)}
			}
			i++
		case c == ')' || c == ']':
			if bracket > 0 {
				bracket--
			}
			i++
		case isOperatorChar(c):
			if !inOperator {
				operators++
				if operators > maxOperators {
					return &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooDeep, Subject: fmt.Sprintf("more than %d chained operators", maxOperators)}
				}
			}
			inOperator = true
			i++
			continue
		default:
			i++
		}
		inOperator = false
	}
	return nil
}

// isOperatorChar reports whether b can be part of a PostgreSQL operator name.
func isOperatorChar(b byte) bool {
	switch b {
	case '+', '-', '*', '/', '<', '>', '=', '~', '!', '@', '#', '%', '^', '&', '|', '`', '?':
		return true
	}
	return false
}

// skipLineComment returns the index just past the end of the line.
func skipLineComment(sql string, i int) int {
	for i < len(sql) && sql[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment returns the index just past the comment's close. Block
// comments nest in PostgreSQL, so an inner open needs its own close. An
// unterminated comment runs to the end; the parser will refuse it.
func skipBlockComment(sql string, i int) int {
	depth := 1
	n := len(sql)
	for i < n && depth > 0 {
		switch {
		case sql[i] == '/' && i+1 < n && sql[i+1] == '*':
			depth++
			i += 2
		case sql[i] == '*' && i+1 < n && sql[i+1] == '/':
			depth--
			i += 2
		default:
			i++
		}
	}
	return i
}

// skipQuoted returns the index just past the closing quote, where a doubled
// quote is an escaped one and stays inside. An unterminated constant runs to
// the end; the parser will refuse it.
func skipQuoted(sql string, i int, quote byte) int {
	n := len(sql)
	for i < n {
		if sql[i] != quote {
			i++
			continue
		}
		if i+1 < n && sql[i+1] == quote {
			i += 2
			continue
		}
		return i + 1
	}
	return i
}

// skipDollarQuoted returns the index just past a dollar-quoted constant
// ($$…$$ or $tag$…$tag$) that opens at i, or i+1 when what is there is not
// an opening tag (a positional parameter, say). An unterminated constant
// runs to the end; the parser will refuse it.
func skipDollarQuoted(sql string, i int) int {
	n := len(sql)
	j := i + 1
	for j < n && (isTagChar(sql[j])) {
		j++
	}
	if j >= n || sql[j] != '$' {
		return i + 1
	}
	tag := sql[i : j+1]
	body := j + 1
	for k := body; k+len(tag) <= n; k++ {
		if sql[k:k+len(tag)] == tag {
			return k + len(tag)
		}
	}
	return n
}

// isTagChar reports whether b may appear in a dollar-quote tag.
func isTagChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

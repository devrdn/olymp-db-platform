package checker

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// MaxGeneratedLength is the length above which a string-building function —
// repeat, lpad, rpad — is refused when the length is written in the query as a
// constant.
//
// This is a first line, not the bound. The bound is a per-process memory limit
// on the game cluster's backends (deploy/docker-compose.yml, ulimits.data): a
// query that over-allocates fails with PostgreSQL's own "out of memory" ERROR
// in that one backend, and the postmaster does not restart. What this constant
// buys is a clear, immediate refusal for the obvious case — a constant length
// nobody could mean — rather than making a participant wait for the query to
// run and fail. Anything the checker cannot read as a constant (a column, a
// subquery, an aggregate, arithmetic, a value-changing cast) is admitted and
// left to the memory limit; that is deliberate, so an ordinary query like
// repeat('#', count(*)::int) is not refused for a size the checker cannot know.
//
// Ten thousand leaves room for every legitimate use (a line of dashes, a
// padded identifier, an aligned column are tens of characters) while refusing
// a constant that is plainly abusive.
const MaxGeneratedLength = 10_000

// MaxSeriesLength is the number of values above which generate_series is
// refused when its bounds are written in the query as constants. The same
// first-line reasoning as MaxGeneratedLength: a series whose bounds are a
// column or a subquery — generate_series(1, (SELECT max(id) FROM t)) — is
// admitted, because the checker cannot know the count without running the
// query. What bounds an admitted runaway is not this constant but the layers
// below: the runner's deadline and its cancellation of the server backend, and
// the per-process memory cap if the series feeds something that allocates.
const MaxSeriesLength = 100_000

// sizeAllowed is the first-line check on the functions that build a value, or
// a series of rows, from a size. It refuses only what it can prove abusive
// from constants written in the query; everything else it admits, leaving the
// game cluster's per-process memory limit as the real bound.
//
// Only the functions whose output size is a plain multiple of a number are
// here. Functions that multiply their input another way — replace,
// regexp_replace, and the like — are not: bounding them would be a growing
// list of special cases, and the memory limit already covers them.
func sizeAllowed(name string, call *pg.FuncCall) error {
	switch name {
	case "repeat", "lpad", "rpad":
		return lengthAllowed(name, call)
	case "generate_series":
		return seriesAllowed(call)
	}
	return nil
}

// lengthAllowed refuses repeat, lpad and rpad when their length argument is a
// constant above MaxGeneratedLength. The length is the second argument of all
// three.
func lengthAllowed(name string, call *pg.FuncCall) error {
	args := call.GetArgs()
	// A VARIADIC array or a named argument cannot be read by position; those
	// are admitted and left to the memory limit rather than guessed at.
	if call.GetFuncVariadic() || len(args) < 2 || args[1].GetNamedArgExpr() != nil {
		return nil
	}
	size, ok := constantNumber(args[1])
	if !ok || size <= MaxGeneratedLength {
		return nil
	}
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeArgumentNotBounded,
		Subject: fmt.Sprintf("%s length %s exceeds the %d limit", name, formatSize(size), MaxGeneratedLength),
	}
}

// seriesAllowed refuses generate_series when its bounds and step are constants
// whose span holds more than MaxSeriesLength values. Only the numeric form is
// read; a date or timestamp series, and any form with a non-constant argument,
// is admitted and left to the memory limit.
func seriesAllowed(call *pg.FuncCall) error {
	args := call.GetArgs()
	if call.GetFuncVariadic() || len(args) < 2 || len(args) > 3 {
		return nil
	}
	for _, arg := range args {
		if arg.GetNamedArgExpr() != nil {
			return nil
		}
	}

	start, ok := constantNumber(args[0])
	if !ok {
		return nil
	}
	stop, ok := constantNumber(args[1])
	if !ok {
		return nil
	}
	step := 1.0
	if len(args) == 3 {
		// A step of zero is a runtime error in PostgreSQL, not a count this
		// can read; admitted and left to fail there.
		if step, ok = constantNumber(args[2]); !ok || step == 0 {
			return nil
		}
	}

	values := seriesCount(start, stop, step)
	if values <= MaxSeriesLength {
		return nil
	}
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeArgumentNotBounded,
		Subject: fmt.Sprintf("generate_series produces %s values, over the %d limit", formatSize(values), MaxSeriesLength),
	}
}

// seriesCount is how many values generate_series(start, stop, step) yields:
// none when the step points away from the stop, one more than the whole steps
// between them otherwise. Non-finite inputs (a constant like 1e400) count as
// over any bound.
func seriesCount(start, stop, step float64) float64 {
	steps := (stop - start) / step
	if math.IsInf(steps, 0) || math.IsNaN(steps) {
		return math.Inf(1)
	}
	if steps < 0 {
		return 0
	}
	return math.Floor(steps) + 1
}

// constantNumber reads a number written in the query as a constant: an integer
// or decimal literal, negated, or cast to a numeric type — nothing else.
//
// A cast is followed only when its target is a numeric type (integer, numeric
// or floating point), because those preserve the value the checker is reading.
// A cast to any other type is not followed: (-173741824)::bit(30)::int reads,
// at runtime, as 900000000 — the bit(30) reinterprets the bits — so following
// it and taking the inner literal would read a size the query does not use.
// Such a cast makes the argument one the checker cannot know, which is admitted
// and left to the memory limit, not read as its inner literal.
func constantNumber(node *pg.Node) (float64, bool) {
	switch {
	case node.GetAConst() != nil:
		c := node.GetAConst()
		switch {
		case c.GetIval() != nil:
			return float64(c.GetIval().GetIval()), true
		case c.GetFval() != nil:
			// A constant too large for a float64 (1e400) parses to infinity
			// with an ErrRange error. It is still a constant the participant
			// wrote, and one plainly above any bound, so it is kept and
			// refused — not read as unreadable and admitted. Only a genuinely
			// unparseable value (which the grammar should never produce for a
			// numeric literal) is treated as not a constant.
			v, err := strconv.ParseFloat(c.GetFval().GetFval(), 64)
			if math.IsNaN(v) || (err != nil && !errors.Is(err, strconv.ErrRange)) {
				return 0, false
			}
			return v, true
		}
		return 0, false
	case node.GetTypeCast() != nil:
		if !numericTypeName(node.GetTypeCast().GetTypeName()) {
			return 0, false
		}
		return constantNumber(node.GetTypeCast().GetArg())
	case node.GetAExpr() != nil:
		// The grammar folds a minus sign into the constant it precedes, so a
		// bare negative literal never reaches here; this catches a minus in
		// front of something else, such as a cast.
		e := node.GetAExpr()
		if e.GetKind() != pg.A_Expr_Kind_AEXPR_OP || e.GetLexpr() != nil || len(e.GetName()) != 1 ||
			e.GetName()[0].GetString_().GetSval() != "-" {
			return 0, false
		}
		v, ok := constantNumber(e.GetRexpr())
		return -v, ok
	}
	return 0, false
}

// numericTypes are PostgreSQL's own names for the integer, numeric and
// floating-point types, which is what the parser normalises every spelling to:
// smallint to int2, integer and int to int4, bigint to int8, decimal to
// numeric, real to float4, double precision to float8.
var numericTypes = names("int2", "int4", "int8", "numeric", "float4", "float8")

// numericTypeName reports whether a cast target is one of those, bare or
// qualified with pg_catalog (both spell the same type).
func numericTypeName(tn *pg.TypeName) bool {
	parts := tn.GetNames()
	if len(parts) == 0 || tn.GetArrayBounds() != nil {
		return false
	}
	var bare string
	switch len(parts) {
	case 1:
		bare = strings.ToLower(parts[0].GetString_().GetSval())
	case 2:
		if strings.ToLower(parts[0].GetString_().GetSval()) != "pg_catalog" {
			return false
		}
		bare = strings.ToLower(parts[1].GetString_().GetSval())
	default:
		return false
	}
	_, ok := numericTypes[bare]
	return ok
}

// formatSize prints a constant the way it was written, as a whole number where
// it is one and with no exponent where it is not, so the refusal names the
// value the participant typed rather than a float's scientific form. A value
// too large for a float64 is named as such rather than printed.
func formatSize(v float64) string {
	if math.IsInf(v, 0) {
		return "a number out of range"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

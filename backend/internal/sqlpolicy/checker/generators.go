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

// MaxGeneratedLength is the constant length above which repeat, lpad and rpad
// are refused. It only gives an early, clear refusal for an obviously abusive
// constant; the real bound is the game cluster's per-process memory limit
// (deploy/docker-compose.yml, ulimits.data), which fails one backend with
// "out of memory". A non-constant length such as count(*)::int is admitted.
const MaxGeneratedLength = 10_000

// MaxSeriesLength is the number of values above which generate_series with
// constant bounds is refused. Non-constant bounds are admitted and left to the
// runner's deadline and the memory limit.
const MaxSeriesLength = 100_000

// sizeAllowed refuses a value or row generator only when constants in the
// query prove its size abusive. Functions that grow their input another way
// (replace, regexp_replace) are left to the memory limit.
func sizeAllowed(name string, call *pg.FuncCall) error {
	switch name {
	case "repeat", "lpad", "rpad":
		return lengthAllowed(name, call)
	case "generate_series":
		return seriesAllowed(call)
	}
	return nil
}

// lengthAllowed checks the length, which is the second argument of all three.
func lengthAllowed(name string, call *pg.FuncCall) error {
	args := call.GetArgs()
	// A VARIADIC array or a named argument cannot be read by position.
	if call.GetFuncVariadic() || len(args) < 2 || args[1].GetNamedArgExpr() != nil {
		return nil
	}
	size, ok := constantNumber(args[1])
	if !ok || size <= MaxGeneratedLength {
		return nil
	}
	if math.IsInf(size, 0) {
		return &sqlpolicy.Refusal{
			Code:    sqlpolicy.CodeArgumentNotBounded,
			Subject: fmt.Sprintf("%s was given a length that is not a real number, far over the %d limit", name, MaxGeneratedLength),
		}
	}
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeArgumentNotBounded,
		Subject: fmt.Sprintf("%s length %s exceeds the %d limit", name, formatSize(size), MaxGeneratedLength),
	}
}

// seriesAllowed reads only the numeric form; a date or timestamp series is
// admitted.
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
		// A zero step is a runtime error in PostgreSQL; left to fail there.
		if step, ok = constantNumber(args[2]); !ok || step == 0 {
			return nil
		}
	}

	values := seriesCount(start, stop, step)
	if values <= MaxSeriesLength {
		return nil
	}
	if math.IsInf(values, 0) {
		return &sqlpolicy.Refusal{
			Code:    sqlpolicy.CodeArgumentNotBounded,
			Subject: fmt.Sprintf("generate_series was given bounds too large to count, far over the %d limit", MaxSeriesLength),
		}
	}
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeArgumentNotBounded,
		Subject: fmt.Sprintf("generate_series produces %s values, over the %d limit", formatSize(values), MaxSeriesLength),
	}
}

// seriesCount is how many values generate_series(start, stop, step) yields.
// Non-finite inputs (a constant like 1e400) count as over any bound.
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
// or decimal literal, negated, or cast to a numeric type. Other casts change
// the value ((-173741824)::bit(30)::int is 900000000), so they make the
// argument unknown rather than being read through.
func constantNumber(node *pg.Node) (float64, bool) {
	switch {
	case node.GetAConst() != nil:
		c := node.GetAConst()
		switch {
		case c.GetIval() != nil:
			return float64(c.GetIval().GetIval()), true
		case c.GetFval() != nil:
			// 1e400 parses to infinity with ErrRange: still a constant, and
			// over any bound, so it is kept rather than treated as unknown.
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
		// A minus before a non-literal, such as a cast; the grammar folds it
		// into a bare literal.
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

// numericTypes are the names the parser normalises every numeric type
// spelling to (integer to int4, double precision to float8, ...).
var numericTypes = names("int2", "int4", "int8", "numeric", "float4", "float8")

// numericTypeName reports whether a cast target is one of those, bare or
// pg_catalog-qualified.
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

// formatSize prints a finite constant without an exponent, as typed.
func formatSize(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

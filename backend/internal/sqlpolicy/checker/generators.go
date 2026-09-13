package checker

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// MaxGeneratedLength bounds the length a string-building function may be
// asked to produce: the count of repeat, the length of lpad and rpad, a width
// inside a format string.
//
// These are the allow-listed functions whose output size is decided by a
// number rather than by the data they are given. PostgreSQL builds the whole
// value inside the server process before a single byte of it is sent, so
// nothing the Query Runner applies to a result — the byte budget, the row
// limit, the deadline — stops the allocation; it only discards what was
// already built. A line of dashes, a padded identifier or an aligned column
// is tens of characters, so ten thousand leaves room for every legitimate use
// and keeps one call a few megabytes even in a four-byte encoding.
const MaxGeneratedLength = 10_000

// MaxSeriesLength bounds how many values one generate_series may produce.
//
// A series is harmless on its own, but it is the row source that turns a
// bounded value into an unbounded aggregate: a string built once is bounded,
// the same string built once per row of a series and folded by string_agg is
// not. A hundred thousand is far beyond a calendar of a year in minutes'
// resolution and far beyond any numbering a detective query needs.
const MaxSeriesLength = 100_000

// sizeAllowed checks the argument that decides how much a generating function
// produces, for the functions that have one. Every other function passes.
//
// The rule is deliberately narrow: the size has to be written in the query as
// a number (optionally negative, optionally cast), and within the bound. A
// column, a parameter, arithmetic, a subquery or another call is refused even
// when its value would be small, because the checker cannot know the value
// without running the query — and a rule that tried to evaluate expressions
// would be a second SQL interpreter to keep correct.
//
// This bounds the values these functions build directly. It does not, and
// cannot, bound everything a query can build: concatenation in a recursive
// CTE doubles a string without calling any of them. That is bounded by the
// per-process memory limit the game cluster runs under (deploy/
// docker-compose.yml), which turns an oversized allocation into an "out of
// memory" error for that one query instead of a killed server.
func sizeAllowed(name string, call *pg.FuncCall) error {
	switch name {
	case "repeat":
		return lengthAllowed(name, call, 2, 2)
	case "lpad", "rpad":
		return lengthAllowed(name, call, 2, 3)
	case "format":
		return formatAllowed(call)
	case "generate_series":
		return seriesAllowed(call)
	}
	return nil
}

// lengthRefusal is the one refusal for a string builder, whatever was wrong
// with the call: the subject is stable per function, so the journal groups
// them, and it carries the bound, so the participant can fix the call.
func lengthRefusal(name string) error {
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeArgumentNotBounded,
		Subject: fmt.Sprintf("%s: at most %d", name, MaxGeneratedLength),
	}
}

func seriesRefusal() error {
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeArgumentNotBounded,
		Subject: fmt.Sprintf("generate_series: at most %d values", MaxSeriesLength),
	}
}

// positional returns the call's arguments when they can be read by position:
// the expected number of them, none named, and no VARIADIC array standing in
// for several.
func positional(call *pg.FuncCall, least, most int) ([]*pg.Node, bool) {
	args := call.GetArgs()
	if call.GetFuncVariadic() || len(args) < least || len(args) > most {
		return nil, false
	}
	for _, arg := range args {
		if arg.GetNamedArgExpr() != nil {
			return nil, false
		}
	}
	return args, true
}

// lengthAllowed checks repeat, lpad and rpad, whose second argument is the
// length of what they build.
func lengthAllowed(name string, call *pg.FuncCall, least, most int) error {
	args, ok := positional(call, least, most)
	if !ok {
		return lengthRefusal(name)
	}
	size, ok := literalNumber(args[1])
	if !ok || size > MaxGeneratedLength {
		return lengthRefusal(name)
	}
	return nil
}

// literalNumber reads a number written in the query: an integer or decimal
// constant, negated or cast any number of times. Anything else is not a
// literal and reports false.
//
// A decimal constant is read as a float64, which is exact for every integer
// that matters to a bound of this size, saturates to infinity rather than
// allocating for a constant like 1e400, and is refused as not finite.
func literalNumber(node *pg.Node) (float64, bool) {
	switch {
	case node.GetAConst() != nil:
		c := node.GetAConst()
		switch {
		case c.GetIval() != nil:
			return float64(c.GetIval().GetIval()), true
		case c.GetFval() != nil:
			v, err := strconv.ParseFloat(c.GetFval().GetFval(), 64)
			if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
				return 0, false
			}
			return v, true
		}
		return 0, false
	case node.GetTypeCast() != nil:
		return literalNumber(node.GetTypeCast().GetArg())
	case node.GetAExpr() != nil:
		// The grammar folds a minus sign into the constant it precedes, so
		// this is reached only for a minus in front of something else — a
		// cast, say. A plus sign is not folded and not accepted.
		e := node.GetAExpr()
		if e.GetKind() != pg.A_Expr_Kind_AEXPR_OP || e.GetLexpr() != nil || len(e.GetName()) != 1 ||
			e.GetName()[0].GetString_().GetSval() != "-" {
			return 0, false
		}
		v, ok := literalNumber(e.GetRexpr())
		return -v, ok
	}
	return 0, false
}

// formatAllowed checks format, whose format string can ask for any width.
//
// The format string has to be written in the query, so that its widths can be
// read; a width taken from an argument (`%*s`) is refused, the same as a
// length argument that is not a literal would be.
func formatAllowed(call *pg.FuncCall) error {
	const name = "format"

	if call.GetFuncVariadic() || len(call.GetArgs()) == 0 || call.GetArgs()[0].GetNamedArgExpr() != nil {
		return lengthRefusal(name)
	}
	spec, ok := literalString(call.GetArgs()[0])
	if !ok || !formatWidthsBounded(spec) {
		return lengthRefusal(name)
	}
	return nil
}

// formatWidthsBounded reads a format string the way PostgreSQL's format()
// does — %[position$][flags][width]type — and reports whether its widths are
// all written as numbers and add up to no more than the bound.
//
// The sum rather than each width: a format string may hold thousands of
// specifiers, and each one pads its own copy. A malformed specifier is left
// to format(), which refuses it.
func formatWidthsBounded(spec string) bool {
	digits := func(i int) int {
		for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
			i++
		}
		return i
	}

	total := 0
	for i := 0; i < len(spec); i++ {
		if spec[i] != '%' {
			continue
		}
		i++
		if i < len(spec) && spec[i] == '%' {
			continue
		}
		// An argument position is digits followed by '$'; digits that are not
		// followed by one are the width itself.
		if end := digits(i); end > i && end < len(spec) && spec[end] == '$' {
			i = end + 1
		}
		for i < len(spec) && spec[i] == '-' {
			i++
		}
		if i < len(spec) && spec[i] == '*' {
			// The width comes from an argument, which is not read.
			return false
		}
		end := digits(i)
		if end > i {
			width := strings.TrimLeft(spec[i:end], "0")
			// Nine digits cannot overflow an int anywhere this runs, and any
			// width that long is past the bound whatever its value.
			if len(width) > 9 {
				return false
			}
			n, _ := strconv.Atoi(width)
			if total += n; total > MaxGeneratedLength {
				return false
			}
		}
		// i now rests on the type character, which the loop's own step skips.
		i = end
	}
	return true
}

// literalString reads a string written in the query, bare or cast.
func literalString(node *pg.Node) (string, bool) {
	if cast := node.GetTypeCast(); cast != nil {
		return literalString(cast.GetArg())
	}
	if c := node.GetAConst(); c != nil && c.GetSval() != nil {
		return c.GetSval().GetSval(), true
	}
	return "", false
}

// seriesAllowed checks generate_series, whose bounds and step decide how many
// rows it produces.
//
// Two forms are read: numbers, where the count is the span divided by the
// step, and dates or timestamps with an interval step, where the step is
// taken at its shortest (a month as 28 days, a year as 365) so that the
// estimate is never below the real count. Every other form is refused.
func seriesAllowed(call *pg.FuncCall) error {
	// Four arguments is the time-zone form of the timestamp series; the fourth
	// names a zone and does not change the count.
	args, ok := positional(call, 2, 4)
	if !ok {
		return seriesRefusal()
	}

	if len(args) <= 3 {
		if count, ok := numericSeries(args); ok {
			if count > MaxSeriesLength {
				return seriesRefusal()
			}
			return nil
		}
	}
	if len(args) >= 3 {
		if count, ok := temporalSeries(args); ok {
			if count > MaxSeriesLength {
				return seriesRefusal()
			}
			return nil
		}
	}
	return seriesRefusal()
}

// numericSeries counts a series whose bounds and step are numbers written in
// the query. A step of zero is reported as unreadable: PostgreSQL refuses it,
// and there is no count to compare.
func numericSeries(args []*pg.Node) (float64, bool) {
	start, ok := literalNumber(args[0])
	if !ok {
		return 0, false
	}
	stop, ok := literalNumber(args[1])
	if !ok {
		return 0, false
	}
	step := 1.0
	if len(args) == 3 {
		if step, ok = literalNumber(args[2]); !ok || step == 0 {
			return 0, false
		}
	}
	return seriesCount((stop - start) / step), true
}

// seriesCount turns the number of steps between the bounds into the number of
// values: none when the step points away from the stop, one more than the
// whole steps otherwise.
func seriesCount(steps float64) float64 {
	if math.IsInf(steps, 0) || math.IsNaN(steps) {
		return math.Inf(1)
	}
	if steps < 0 {
		return 0
	}
	return math.Floor(steps) + 1
}

// temporalSeries counts a series of dates or timestamps written in the query,
// with an interval step written in the query.
func temporalSeries(args []*pg.Node) (float64, bool) {
	start, ok := literalTimestamp(args[0])
	if !ok {
		return 0, false
	}
	stop, ok := literalTimestamp(args[1])
	if !ok {
		return 0, false
	}
	step, ok := literalInterval(args[2])
	if !ok || step == 0 {
		return 0, false
	}
	span := float64(stop.Unix() - start.Unix())
	return seriesCount(span / float64(step)), true
}

// timestampLayouts are the spellings of a date or timestamp literal that are
// read. A spelling outside them — 'infinity', 'today', an era, a five-digit
// year — is not read, and the series is refused rather than guessed at.
var timestampLayouts = func() []string {
	var layouts []string
	for _, clock := range []string{"", " 15:04", " 15:04:05", " 15:04:05.999999", "T15:04", "T15:04:05", "T15:04:05.999999"} {
		for _, zone := range []string{"", "Z07", "Z07:00", "Z0700"} {
			if clock == "" && zone != "" {
				continue
			}
			layouts = append(layouts, "2006-01-02"+clock+zone)
		}
	}
	return layouts
}()

// literalTimestamp reads a date or timestamp written in the query: a bare
// string, or one cast to date, timestamp or timestamptz.
func literalTimestamp(node *pg.Node) (time.Time, bool) {
	text, ok := typedString(node, "date", "timestamp", "timestamptz")
	if !ok {
		return time.Time{}, false
	}
	text = strings.TrimSpace(text)
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, text); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// intervalUnits are the interval units a step may be written in, each at its
// shortest length in seconds.
var intervalUnits = map[string]int64{
	"second": 1, "sec": 1,
	"minute": 60, "min": 60,
	"hour": 3600,
	"day":  86400,
	"week": 7 * 86400,
	// The shortest month and the shortest year, so a count computed with them
	// is never below the real one.
	"month": 28 * 86400, "mon": 28 * 86400,
	"year": 365 * 86400,
}

var (
	intervalAmount = regexp.MustCompile(`^([+-]?\d{1,9})\s*([a-z]+)$`)
	intervalClock  = regexp.MustCompile(`^(\d{1,4}):(\d{2})(?::(\d{2}))?$`)
)

// literalInterval reads an interval step written in the query, as a bare
// string or one cast to interval, in seconds. Two spellings are read: one
// amount and one unit ('15 minutes', '1 day'), and a clock ('01:30:00').
// Anything else — several parts, fractions, ISO 8601 — is not read.
func literalInterval(node *pg.Node) (int64, bool) {
	text, ok := typedString(node, "interval")
	if !ok {
		return 0, false
	}
	text = strings.ToLower(strings.TrimSpace(text))

	if m := intervalAmount.FindStringSubmatch(text); m != nil {
		amount, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, false
		}
		unit, known := intervalUnits[strings.TrimSuffix(m[2], "s")]
		if !known {
			return 0, false
		}
		return amount * unit, true
	}
	if m := intervalClock.FindStringSubmatch(text); m != nil {
		hours, _ := strconv.ParseInt(m[1], 10, 64)
		minutes, _ := strconv.ParseInt(m[2], 10, 64)
		seconds, _ := strconv.ParseInt(m[3], 10, 64)
		return hours*3600 + minutes*60 + seconds, true
	}
	return 0, false
}

// typedString reads a string constant, bare or cast to one of the named
// types, whether or not the cast is qualified with pg_catalog.
func typedString(node *pg.Node, types ...string) (string, bool) {
	if cast := node.GetTypeCast(); cast != nil {
		names := cast.GetTypeName().GetNames()
		if len(names) == 0 || cast.GetTypeName().GetArrayBounds() != nil {
			return "", false
		}
		last := strings.ToLower(names[len(names)-1].GetString_().GetSval())
		matched := false
		for _, t := range types {
			matched = matched || last == t
		}
		if !matched {
			return "", false
		}
		node = cast.GetArg()
	}
	if c := node.GetAConst(); c != nil && c.GetSval() != nil {
		return c.GetSval().GetSval(), true
	}
	return "", false
}

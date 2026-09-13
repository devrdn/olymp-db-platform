package checker_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

// unbounded asserts the query is refused because a generating function's size
// is not one the checker can bound, and that the refusal names the function.
func unbounded(t *testing.T, sql, function string, p sqlpolicy.Policy) {
	t.Helper()

	r := refusal(t, sql, p)
	if r.Code != sqlpolicy.CodeArgumentNotBounded {
		t.Fatalf("%s\n  code = %q (%s), want %q", sql, r.Code, r.Subject, sqlpolicy.CodeArgumentNotBounded)
	}
	if !strings.HasPrefix(r.Subject, function) {
		t.Fatalf("%s\n  subject = %q, want it to name %s", sql, r.Subject, function)
	}
}

// The ordinary uses of the functions that build something from a number: a
// padded identifier, a separator line, a short calendar. Each has its size
// written in the query and well inside the bound, and none of them may
// regress while the unbounded shapes are refused.
func TestBoundedGeneratorsStayAvailable(t *testing.T) {
	for _, sql := range []string{
		`SELECT repeat('-', 20)`,
		`SELECT repeat(name, 3) FROM suspects`,
		`SELECT pg_catalog.repeat('=', 40)`,
		`SELECT repeat('x', 5::bigint)`,
		`SELECT repeat('x', CAST(5 AS integer))`,
		`SELECT repeat('x', -1)`,
		`SELECT lpad(id::text, 6, '0') FROM suspects`,
		`SELECT rpad(name, 20) FROM suspects`,
		`SELECT rpad(name, 20, '.') || city FROM suspects`,
		`SELECT format('%s lives in %s', name, city) FROM suspects`,
		`SELECT format('%-20s|%10s', name, city) FROM suspects`,
		`SELECT format('%1$s and %1$s again, %%', name) FROM suspects`,
		`SELECT format('%I.%L', 'public', name) FROM suspects`,
		`SELECT generate_series(1, 5)`,
		`SELECT * FROM generate_series(10, 1, -1)`,
		`SELECT * FROM generate_series(0, 100, 5) AS g`,
		`SELECT * FROM generate_series(0.5, 10, 0.5)`,
		`SELECT d::date FROM generate_series('2024-01-01'::date, '2024-12-31'::date, '1 day') AS d`,
		`SELECT * FROM generate_series(timestamp '2024-03-01 08:00', timestamp '2024-03-08 20:00', interval '15 minutes')`,
		`SELECT * FROM generate_series('2024-01-01', '2024-01-02', '01:00:00'::interval)`,
		`SELECT * FROM generate_series('2020-01-01'::timestamptz, '2024-01-01'::timestamptz, '1 month')`,
		`SELECT * FROM generate_series('2024-01-01 00:00:00+03'::timestamptz, '2024-01-07 00:00:00+03'::timestamptz, '1 hour', 'Europe/Chisinau')`,
		`SELECT s.day, count(v.id) FROM generate_series('2024-05-01'::date, '2024-05-31'::date, '1 day') AS s(day) LEFT JOIN sightings v ON v.seen_at::date = s.day GROUP BY s.day`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

// The edge of each bound is inside it, and one past the edge is outside.
func TestTheBoundsAreInclusive(t *testing.T) {
	length := strconv.Itoa(checker.MaxGeneratedLength)
	past := strconv.Itoa(checker.MaxGeneratedLength + 1)

	allow(t, `SELECT repeat('x', `+length+`)`, sqlpolicy.ReadOnly())
	allow(t, `SELECT lpad('x', `+length+`)`, sqlpolicy.ReadOnly())
	allow(t, `SELECT rpad('x', `+length+`, 'y')`, sqlpolicy.ReadOnly())
	allow(t, `SELECT format('%`+length+`s', 'x')`, sqlpolicy.ReadOnly())
	unbounded(t, `SELECT repeat('x', `+past+`)`, "repeat", sqlpolicy.ReadOnly())
	unbounded(t, `SELECT lpad('x', `+past+`)`, "lpad", sqlpolicy.ReadOnly())
	unbounded(t, `SELECT rpad('x', `+past+`, 'y')`, "rpad", sqlpolicy.ReadOnly())
	unbounded(t, `SELECT format('%`+past+`s', 'x')`, "format", sqlpolicy.ReadOnly())
	// The widths of one format string add up: each specifier pads its own
	// copy, so a thousand of them within the bound one by one are not.
	half := strconv.Itoa(checker.MaxGeneratedLength / 2)
	allow(t, `SELECT format('%`+half+`s%`+half+`s', 'x', 'y')`, sqlpolicy.ReadOnly())
	unbounded(t, `SELECT format('%`+half+`s%`+half+`s%1s', 'x', 'y', 'z')`, "format", sqlpolicy.ReadOnly())
	unbounded(t, `SELECT format('`+strings.Repeat("%1$9999s", 1000)+`', 'x')`, "format", sqlpolicy.ReadOnly())

	series := strconv.Itoa(checker.MaxSeriesLength)
	allow(t, `SELECT count(*) FROM generate_series(1, `+series+`)`, sqlpolicy.ReadOnly())
	allow(t, `SELECT count(*) FROM generate_series(`+series+`, 1, -1)`, sqlpolicy.ReadOnly())
	unbounded(t, `SELECT count(*) FROM generate_series(1, `+strconv.Itoa(checker.MaxSeriesLength+1)+`)`,
		"generate_series", sqlpolicy.ReadOnly())
	unbounded(t, `SELECT count(*) FROM generate_series(0, `+series+`)`, "generate_series", sqlpolicy.ReadOnly())
	// The step divides the span: a million with a step of ten is a hundred
	// thousand values, and with a step of nine it is more.
	allow(t, `SELECT count(*) FROM generate_series(1, 1000000, 10)`, sqlpolicy.ReadOnly())
	unbounded(t, `SELECT count(*) FROM generate_series(1, 1000000, 9)`, "generate_series", sqlpolicy.ReadOnly())
}

// A size written as a large number is the plain case; the others are every
// way of writing a size the checker cannot read without running the query.
func TestAnUnboundedSizeIsRefused(t *testing.T) {
	for sql, function := range map[string]string{
		// Far past the bound.
		`SELECT length(repeat('x', 900000000))`:     "repeat",
		`SELECT repeat('x', 3000000000)`:            "repeat",
		`SELECT rpad('', 900000000, 'x')`:           "rpad",
		`SELECT lpad('', 900000000)`:                "lpad",
		`SELECT format('%900000000s', '')`:          "format",
		`SELECT format('%1$-900000000s', 'x')`:      "format",
		`SELECT format('%000000000000020001s', '')`: "format",
		// A column, a parameter, arithmetic, a subquery, a function call.
		`SELECT repeat('x', age) FROM suspects`:                                                 "repeat",
		`SELECT repeat('x', $1)`:                                                                "repeat",
		`SELECT repeat('x', 5 + 5)`:                                                             "repeat",
		`SELECT repeat('x', 10 * 1000)`:                                                         "repeat",
		`SELECT repeat('x', +5)`:                                                                "repeat",
		`SELECT repeat('x', (SELECT 5))`:                                                        "repeat",
		`SELECT repeat('x', length('abc'))`:                                                     "repeat",
		`SELECT repeat('x', '5')`:                                                               "repeat",
		`SELECT repeat('x', '5'::int)`:                                                          "repeat",
		`SELECT lpad(name, age) FROM suspects`:                                                  "lpad",
		`SELECT rpad(name, max_len, '.') FROM suspects`:                                         "rpad",
		`SELECT format('%*s', 900000000, '')`:                                                   "format",
		`SELECT format('%1$*2$s', 'x', 900000000)`:                                              "format",
		`SELECT format(template, name) FROM suspects`:                                           "format",
		`SELECT format('%s' || '%900000000s', 'a', 'b')`:                                        "format",
		`SELECT generate_series(1, n) FROM suspects`:                                            "generate_series",
		`SELECT * FROM generate_series(1, 1000000)`:                                             "generate_series",
		`SELECT * FROM generate_series(1, 10, 0)`:                                               "generate_series",
		`SELECT * FROM generate_series(0, 1, 0.000001)`:                                         "generate_series",
		`SELECT * FROM generate_series(1, 2 * 100000)`:                                          "generate_series",
		`SELECT * FROM generate_series(1, (SELECT max(id) FROM suspects))`:                      "generate_series",
		`SELECT * FROM generate_series(1, 1e400)`:                                               "generate_series",
		`SELECT * FROM generate_series(now(), now() + interval '1 day', '1 second')`:            "generate_series",
		`SELECT * FROM generate_series('2000-01-01'::timestamp, '2024-01-01', '1 minute')`:      "generate_series",
		`SELECT * FROM generate_series('2024-01-01'::timestamp, 'infinity', '1 day')`:           "generate_series",
		`SELECT g FROM t, generate_series('2024-01-01'::date, '2024-02-01'::date, t.step) g`:    "generate_series",
		`SELECT * FROM generate_series('2024-01-01'::date, '2024-02-01'::date, '0 days')`:       "generate_series",
		`SELECT * FROM generate_series('2024-01-01'::date, '2024-02-01'::date, '1 day 1 hour')`: "generate_series",
		`SELECT * FROM generate_series('2024-01-01'::date, '2024-02-01'::date)`:                 "generate_series",
		// Shapes whose arguments cannot be read positionally.
		`SELECT repeat(VARIADIC ARRAY['x'])`: "repeat",
		`SELECT lpad(string => 'x', 5)`:      "lpad",
		`SELECT repeat('x')`:                 "repeat",
		`SELECT generate_series(1)`:          "generate_series",
	} {
		t.Run(sql, func(t *testing.T) { unbounded(t, sql, function, sqlpolicy.ReadOnly()) })
	}
}

// A check that only looked at the select list would pass every one of these.
// The aggregates matter most: a value built once is bounded, the same value
// built once per row of a large series and folded into one string or array is
// not.
func TestAnUnboundedGeneratorIsFoundWhereverItHides(t *testing.T) {
	for name, sql := range map[string]string{
		"inside string_agg":     `SELECT string_agg(repeat('x', 1000000), ',') FROM suspects`,
		"inside array_agg":      `SELECT array_agg(repeat('x', 1000000)) FROM suspects`,
		"under an aggregate":    `SELECT string_agg(g::text, ',') FROM generate_series(1, 2000000) g`,
		"inside json_agg":       `SELECT json_agg(rpad('', 900000000)) FROM suspects`,
		"in a CTE":              `WITH big AS (SELECT repeat('x', 900000000) AS s) SELECT length(s) FROM big`,
		"in a recursive CTE":    `WITH RECURSIVE r(s) AS (SELECT repeat('x', 900000000) UNION ALL SELECT s FROM r) SELECT 1`,
		"in a subquery":         `SELECT (SELECT length(repeat('x', 900000000)))`,
		"in a FROM subquery":    `SELECT * FROM (SELECT repeat('x', 900000000)) q`,
		"in a lateral":          `SELECT * FROM suspects s, LATERAL generate_series(1, s.age) g`,
		"in a WHERE":            `SELECT 1 FROM suspects WHERE length(repeat(name, 900000000)) > 0`,
		"in ORDER BY":           `SELECT name FROM suspects ORDER BY repeat(name, age)`,
		"in a window":           `SELECT string_agg(repeat('x', 900000000), '') OVER () FROM suspects`,
		"inside another call":   `SELECT upper(lpad('', 900000000))`,
		"inside a bounded call": `SELECT repeat(repeat('x', 900000000), 2)`,
		"behind a cast":         `SELECT repeat('x', 900000000)::varchar`,
		"under EXPLAIN":         `EXPLAIN SELECT repeat('x', 900000000)`,
		"in a UNION arm":        `SELECT 'a' UNION ALL SELECT repeat('x', 900000000)`,
		"in ROWS FROM":          `SELECT * FROM ROWS FROM (generate_series(1, 10000000))`,
	} {
		t.Run(name, func(t *testing.T) {
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Code != sqlpolicy.CodeArgumentNotBounded {
				t.Fatalf("code = %q (%s), want %q", r.Code, r.Subject, sqlpolicy.CodeArgumentNotBounded)
			}
		})
	}

	t.Run("in a column default", func(t *testing.T) {
		p := sqlpolicy.ReadWrite()
		p.AllowOwnTables = true
		unbounded(t, `CREATE TABLE work.notes (body text DEFAULT repeat('x', 900000000))`, "repeat", p)
	})
	t.Run("in a permitted write", func(t *testing.T) {
		unbounded(t, `INSERT INTO evidence (note) SELECT repeat('x', 900000000)`, "repeat", sqlpolicy.ReadWrite("evidence"))
	})
}

// The subject says what the bound is, so the participant can rewrite the
// call without guessing, and the journal groups one refusal per function
// rather than one per number somebody typed.
func TestARefusalNamesTheFunctionAndTheBound(t *testing.T) {
	for sql, want := range map[string]string{
		`SELECT repeat('x', 900000000)`:         "repeat: at most " + strconv.Itoa(checker.MaxGeneratedLength),
		`SELECT repeat('x', age) FROM suspects`: "repeat: at most " + strconv.Itoa(checker.MaxGeneratedLength),
		`SELECT * FROM generate_series(1, 1e7)`: "generate_series: at most " + strconv.Itoa(checker.MaxSeriesLength) + " values",
	} {
		r := refusal(t, sql, sqlpolicy.ReadOnly())
		if r.Subject != want {
			t.Errorf("%s\n  subject = %q, want %q", sql, r.Subject, want)
		}
	}
}

// An operator extending the allow-list does not also lift the bounds on the
// functions already on it.
func TestAnExtendedListKeepsTheBounds(t *testing.T) {
	err := checker.NewChecker("soundex").Check(`SELECT repeat('x', 900000000)`, sqlpolicy.ReadOnly())
	if err == nil {
		t.Fatal("an extended checker allowed an unbounded repeat")
	}
}

// Aggregates over the game's own rows are the contest's ordinary material and
// stay allowed: what bounds them is the data the organiser loaded and the
// memory the game cluster gives each process, not the checker.
func TestAggregatesOverRealRowsStayAllowed(t *testing.T) {
	for _, sql := range []string{
		`SELECT string_agg(name, ', ' ORDER BY name) FROM suspects`,
		`SELECT city, array_agg(name) FROM suspects GROUP BY city`,
		`SELECT json_agg(s) FROM suspects s`,
		`SELECT string_agg(repeat('*', 3) || name, E'\n') FROM suspects`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

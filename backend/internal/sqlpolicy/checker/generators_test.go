package checker_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

// overBound asserts a CodeArgumentNotBounded refusal naming function.
func overBound(t *testing.T, sql, function string, p sqlpolicy.Policy) {
	t.Helper()

	r := refusal(t, sql, p)
	if r.Code != sqlpolicy.CodeArgumentNotBounded {
		t.Fatalf("%s\n  code = %q (%s), want %q", sql, r.Code, r.Subject, sqlpolicy.CodeArgumentNotBounded)
	}
	if !strings.Contains(r.Subject, function) {
		t.Fatalf("%s\n  subject = %q, want it to name %s", sql, r.Subject, function)
	}
}

func TestGeneratorsWithASmallConstantStayAvailable(t *testing.T) {
	for _, sql := range []string{
		`SELECT repeat('-', 20)`,
		`SELECT repeat('x', 5::bigint)`,
		`SELECT repeat('x', CAST(5 AS integer))`,
		`SELECT repeat('x', -1)`,
		`SELECT lpad(id::text, 6, '0') FROM suspects`,
		`SELECT rpad(name, 20) FROM suspects`,
		`SELECT rpad(name, 20, '.') || city FROM suspects`,
		`SELECT generate_series(1, 5)`,
		`SELECT * FROM generate_series(10, 1, -1)`,
		`SELECT * FROM generate_series(0, 100, 5) AS g`,
		`SELECT * FROM generate_series(0.5, 10, 0.5)`,
		`SELECT d::date FROM generate_series('2024-01-01'::date, '2024-12-31'::date, '1 day') AS d`,
		`SELECT * FROM generate_series(timestamp '2024-03-01', timestamp '2024-03-08', interval '1 day')`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

// These are ordinary queries; the game cluster's memory limit, not the
// checker, stops one that turns out abusive.
func TestANonConstantSizeIsAdmitted(t *testing.T) {
	for name, sql := range map[string]string{
		"repeat over a column":           `SELECT repeat('x', age) FROM suspects`,
		"repeat over an aggregate":       `SELECT repeat('#', count(*)::int) FROM suspects GROUP BY city`,
		"repeat over a subquery":         `SELECT repeat('#', (SELECT count(*) FROM suspects)::int)`,
		"repeat over arithmetic":         `SELECT repeat('x', 5 + 5)`,
		"repeat over a parameter":        `SELECT repeat('x', $1)`,
		"repeat over a function call":    `SELECT repeat('x', length('abc'))`,
		"repeat over a string cast":      `SELECT repeat('x', '5'::int)`,
		"lpad over a column":             `SELECT lpad(name, age) FROM suspects`,
		"rpad over a column":             `SELECT rpad(name, max_len, '.') FROM suspects`,
		"series to a subquery bound":     `SELECT generate_series(1, (SELECT max(id) FROM suspects))`,
		"series over columns":            `SELECT generate_series(1, n) FROM suspects`,
		"series over arithmetic":         `SELECT * FROM generate_series(1, 2 * 100000)`,
		"date series between subqueries": `SELECT g::date FROM generate_series((SELECT min(seen_at) FROM sightings)::date, (SELECT max(seen_at) FROM sightings)::date, '1 day') AS g`,
		// Admitted as unknown, not read as its inner literal.
		"repeat behind a bit cast":  `SELECT repeat('x', (-173741824)::bit(30)::int)`,
		"repeat behind a text cast": `SELECT repeat('x', ('9'||'9')::int)`,
	} {
		t.Run(name, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

// The limit itself is allowed.
func TestAConstantOverTheLimitIsRefused(t *testing.T) {
	length := strconv.Itoa(checker.MaxGeneratedLength)
	past := strconv.Itoa(checker.MaxGeneratedLength + 1)

	allow(t, `SELECT repeat('x', `+length+`)`, sqlpolicy.ReadOnly())
	allow(t, `SELECT lpad('x', `+length+`)`, sqlpolicy.ReadOnly())
	allow(t, `SELECT rpad('x', `+length+`, 'y')`, sqlpolicy.ReadOnly())
	overBound(t, `SELECT repeat('x', `+past+`)`, "repeat", sqlpolicy.ReadOnly())
	overBound(t, `SELECT lpad('x', `+past+`)`, "lpad", sqlpolicy.ReadOnly())
	overBound(t, `SELECT rpad('x', `+past+`, 'y')`, "rpad", sqlpolicy.ReadOnly())

	for _, sql := range []string{
		`SELECT repeat('x', 900000000)`,
		`SELECT repeat('x', 3000000000)`,
		`SELECT rpad('', 900000000, 'x')`,
		`SELECT lpad('', 900000000)`,
		`SELECT repeat('x', 20000::bigint)`,
	} {
		t.Run(sql, func(t *testing.T) { overBound(t, sql, "", sqlpolicy.ReadOnly()) })
	}

	// Too large for a float64 is still a constant over the bound, and the
	// subject must not print an infinity.
	rInf := refusal(t, `SELECT repeat('x', 1e400)`, sqlpolicy.ReadOnly())
	if rInf.Code != sqlpolicy.CodeArgumentNotBounded || !strings.Contains(rInf.Subject, "not a real number") {
		t.Fatalf("repeat 1e400 subject = %q", rInf.Subject)
	}
	sInf := refusal(t, `SELECT count(*) FROM generate_series(1, 1e400)`, sqlpolicy.ReadOnly())
	if sInf.Code != sqlpolicy.CodeArgumentNotBounded || !strings.Contains(sInf.Subject, "too large to count") {
		t.Fatalf("generate_series 1e400 subject = %q", sInf.Subject)
	}

	series := strconv.Itoa(checker.MaxSeriesLength)
	allow(t, `SELECT count(*) FROM generate_series(1, `+series+`)`, sqlpolicy.ReadOnly())
	allow(t, `SELECT count(*) FROM generate_series(`+series+`, 1, -1)`, sqlpolicy.ReadOnly())
	overBound(t, `SELECT count(*) FROM generate_series(1, `+strconv.Itoa(checker.MaxSeriesLength+1)+`)`,
		"generate_series", sqlpolicy.ReadOnly())
	overBound(t, `SELECT count(*) FROM generate_series(0, `+series+`)`, "generate_series", sqlpolicy.ReadOnly())
	// The step divides the span.
	allow(t, `SELECT count(*) FROM generate_series(1, 1000000, 10)`, sqlpolicy.ReadOnly())
	overBound(t, `SELECT count(*) FROM generate_series(1, 1000000, 9)`, "generate_series", sqlpolicy.ReadOnly())
}

func TestAConstantOverTheLimitIsFoundWhereverItHides(t *testing.T) {
	for name, sql := range map[string]string{
		"inside string_agg":   `SELECT string_agg(repeat('x', 1000000), ',') FROM suspects`,
		"inside array_agg":    `SELECT array_agg(repeat('x', 1000000)) FROM suspects`,
		"under an aggregate":  `SELECT string_agg(g::text, ',') FROM generate_series(1, 2000000) g`,
		"in a CTE":            `WITH big AS (SELECT repeat('x', 900000000) AS s) SELECT length(s) FROM big`,
		"in a subquery":       `SELECT (SELECT length(repeat('x', 900000000)))`,
		"in a WHERE":          `SELECT 1 FROM suspects WHERE length(repeat(name, 900000000)) > 0`,
		"inside another call": `SELECT upper(lpad('', 900000000))`,
		"behind a cast":       `SELECT repeat('x', 900000000)::varchar`,
		"under EXPLAIN":       `EXPLAIN SELECT repeat('x', 900000000)`,
		"in a UNION arm":      `SELECT 'a' UNION ALL SELECT repeat('x', 900000000)`,
		"in ROWS FROM":        `SELECT * FROM ROWS FROM (generate_series(1, 10000000))`,
	} {
		t.Run(name, func(t *testing.T) {
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Code != sqlpolicy.CodeArgumentNotBounded {
				t.Fatalf("code = %q (%s), want %q", r.Code, r.Subject, sqlpolicy.CodeArgumentNotBounded)
			}
		})
	}

	t.Run("in a permitted write", func(t *testing.T) {
		overBound(t, `INSERT INTO evidence (note) SELECT repeat('x', 900000000)`, "repeat", sqlpolicy.ReadWrite("evidence"))
	})
}

func TestARefusalNamesTheConstantAndTheLimit(t *testing.T) {
	for sql, want := range map[string]string{
		`SELECT repeat('x', 900000000)`:             "repeat length 900000000 exceeds the " + strconv.Itoa(checker.MaxGeneratedLength) + " limit",
		`SELECT * FROM generate_series(1, 1000000)`: "generate_series produces 1000000 values, over the " + strconv.Itoa(checker.MaxSeriesLength) + " limit",
	} {
		r := refusal(t, sql, sqlpolicy.ReadOnly())
		if r.Subject != want {
			t.Errorf("%s\n  subject = %q, want %q", sql, r.Subject, want)
		}
	}
}

func TestAnExtendedListKeepsTheFirstLine(t *testing.T) {
	if err := checker.NewChecker("soundex").Check(`SELECT repeat('x', 900000000)`, sqlpolicy.ReadOnly()); err == nil {
		t.Fatal("an extended checker allowed a repeat with a plainly abusive constant")
	}
}

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

package checker_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

func refusal(t *testing.T, sql string, p sqlpolicy.Policy) *sqlpolicy.Refusal {
	t.Helper()

	err := checker.Check(sql, p)
	if err == nil {
		t.Fatalf("allowed: %s", sql)
	}
	var r *sqlpolicy.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a sqlpolicy.Refusal", err)
	}
	return r
}

func allow(t *testing.T, sql string, p sqlpolicy.Policy) {
	t.Helper()

	if err := checker.Check(sql, p); err != nil {
		t.Fatalf("refused a legitimate query: %s\n  %v", sql, err)
	}
}

// A checker that refuses these is useless however safe it is.
func TestTheQueriesTheContestIsMadeOf(t *testing.T) {
	for _, sql := range []string{
		`SELECT 1`,
		`SELECT name, city FROM suspects WHERE city = 'Chisinau' ORDER BY name LIMIT 10`,
		`SELECT s.name, c.title FROM suspects s JOIN cases c ON c.id = s.case_id`,
		`SELECT s.name FROM suspects s LEFT JOIN alibis a ON a.suspect_id = s.id WHERE a.id IS NULL`,
		`SELECT city, count(*) FROM suspects GROUP BY city HAVING count(*) > 1`,
		`SELECT name, rank() OVER (PARTITION BY city ORDER BY age DESC) FROM suspects`,
		`WITH seen AS (SELECT suspect_id FROM sightings) SELECT * FROM suspects WHERE id IN (SELECT suspect_id FROM seen)`,
		`SELECT name FROM suspects WHERE EXISTS (SELECT 1 FROM alibis WHERE suspect_id = suspects.id)`,
		`SELECT CASE WHEN age > 40 THEN 'older' ELSE 'younger' END FROM suspects`,
		`SELECT coalesce(nickname, name) FROM suspects`,
		`SELECT upper(name), length(name), substring(name FROM 1 FOR 3) FROM suspects`,
		`SELECT age::text FROM suspects`,
		`SELECT name FROM suspects UNION SELECT title FROM cases`,
		`SELECT * FROM suspects EXCEPT SELECT * FROM cleared`,
		`SELECT * FROM (SELECT name FROM suspects) q`,
		`SELECT generate_series(1, 5)`,
		`SELECT g FROM generate_series(1, 5) AS g`,
		`SELECT * FROM generate_series(1, 5)`,
		`SELECT x FROM unnest(ARRAY[1, 2, 3]) AS t(x)`,
		`SELECT s.name, x FROM suspects s, unnest(ARRAY[1, 2]) AS x`,
		`SELECT * FROM suspects s JOIN LATERAL (SELECT 1) q ON true`,
		`SELECT extract(year FROM seen_at) FROM sightings`,
		`SELECT count(DISTINCT city) FROM suspects`,
		`SELECT array_agg(name) FROM suspects`,
		`SELECT * FROM suspects WHERE name LIKE 'A%' AND age BETWEEN 20 AND 30`,
		`SELECT * FROM suspects ORDER BY age DESC NULLS LAST OFFSET 5 LIMIT 5`,
		`SELECT now(), current_date, current_timestamp, localtime`,
		`SELECT * FROM sightings WHERE seen_at > now() - interval '1 day'`,
		`SELECT nullif(city, ''), greatest(age, 18), least(age, 65) FROM suspects`,
		`SELECT name FROM suspects WHERE age = ANY (ARRAY[20, 30])`,
		`SELECT name, sum(x) OVER w FROM t WINDOW w AS (PARTITION BY city)`,
		`SELECT string_agg(name, ', ' ORDER BY name) FROM suspects`,
		`SELECT * FROM suspects s WHERE s.name IS NOT NULL AND s.age IS DISTINCT FROM 0`,
		`EXPLAIN SELECT * FROM suspects`,
		`EXPLAIN (FORMAT JSON, VERBOSE) SELECT * FROM suspects`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

func TestOnlyOneStatement(t *testing.T) {
	r := refusal(t, `SELECT 1; DROP TABLE suspects`, sqlpolicy.ReadOnly())
	if r.Code != sqlpolicy.CodeNotOneStatement {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeNotOneStatement)
	}
}

func TestNothingToRun(t *testing.T) {
	for _, sql := range []string{"", "   ", "-- just a comment", "/* nothing */", ";"} {
		t.Run(sql, func(t *testing.T) {
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Code != sqlpolicy.CodeNotOneStatement {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeNotOneStatement)
			}
		})
	}
}

func TestAQueryThatDoesNotParse(t *testing.T) {
	sql := `SELEC * FROM suspects`
	r := refusal(t, sql, sqlpolicy.ReadOnly())
	if r.Code != sqlpolicy.CodeParseError {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeParseError)
	}
	// The parser's own message is carried, not replaced.
	if !strings.Contains(strings.ToLower(r.Subject), "selec") {
		t.Fatalf("the refusal does not carry the parser's message: %q", r.Subject)
	}
	// A real character offset into sql, not just a nonzero placeholder.
	if r.Position <= 0 || r.Position > len(sql) {
		t.Fatalf("position = %d, want a 1-based offset into %q (len %d)", r.Position, sql, len(sql))
	}
}

// Each is a way to change the database, read beyond the game, or spend the
// server's time.
func TestReadOnlyRefusesEveryWayOfWriting(t *testing.T) {
	for _, sql := range []string{
		`INSERT INTO suspects (name) VALUES ('x')`,
		`UPDATE suspects SET name = 'x'`,
		`DELETE FROM suspects`,
		`TRUNCATE suspects`,
		`DROP TABLE suspects`,
		`ALTER TABLE suspects ADD COLUMN x int`,
		`CREATE TABLE notes (a int)`,
		`CREATE TEMP TABLE notes (a int)`,
		`CREATE VIEW v AS SELECT 1`,
		`CREATE INDEX ON suspects (name)`,
		`GRANT SELECT ON suspects TO PUBLIC`,
		`CREATE ROLE hacker LOGIN SUPERUSER`,
		`COPY suspects FROM '/etc/passwd'`,
		`COPY (SELECT 1) TO '/tmp/out'`,
		`SET statement_timeout = 0`,
		`RESET ALL`,
		`DO $$ BEGIN PERFORM 1; END $$`,
		`CALL something()`,
		`VACUUM suspects`,
		`LOCK TABLE suspects`,
		`BEGIN`,
		`COMMIT`,
		`CREATE EXTENSION dblink`,
		`LISTEN channel`,
		`PREPARE p AS SELECT 1`,
		`EXECUTE p`,
		`CREATE FUNCTION f() RETURNS int AS 'SELECT 1' LANGUAGE sql`,
	} {
		t.Run(sql, func(t *testing.T) {
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Code != sqlpolicy.CodeStatementNotSupported {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeStatementNotSupported)
			}
		})
	}
}

// A root-node check alone would let these through.
func TestTheWritesDisguisedAsReads(t *testing.T) {
	t.Run("SELECT INTO creates a table", func(t *testing.T) {
		refusal(t, `SELECT * INTO notes FROM suspects`, sqlpolicy.ReadOnly())
	})

	t.Run("a data-modifying CTE", func(t *testing.T) {
		refusal(t,
			`WITH gone AS (DELETE FROM suspects RETURNING *) SELECT * FROM gone`,
			sqlpolicy.ReadOnly())
	})

	t.Run("EXPLAIN ANALYZE executes what it explains", func(t *testing.T) {
		refusal(t, `EXPLAIN ANALYZE DELETE FROM suspects`, sqlpolicy.ReadOnly())
		refusal(t, `EXPLAIN (ANALYZE) SELECT * FROM suspects`, sqlpolicy.ReadOnly())
	})

	t.Run("EXPLAIN carries only the options that change the printout", func(t *testing.T) {
		// SETTINGS prints server configuration the catalog rules hide.
		refusal(t, `EXPLAIN (SETTINGS) SELECT * FROM suspects`, sqlpolicy.ReadOnly())
		refusal(t, `EXPLAIN (BUFFERS) SELECT * FROM suspects`, sqlpolicy.ReadOnly())
		refusal(t, `EXPLAIN (WAL) SELECT * FROM suspects`, sqlpolicy.ReadOnly())
		allow(t, `EXPLAIN (VERBOSE, COSTS false, FORMAT JSON) SELECT * FROM suspects`, sqlpolicy.ReadOnly())
	})

	t.Run("locking is a write to the transaction", func(t *testing.T) {
		refusal(t, `SELECT * FROM suspects FOR UPDATE`, sqlpolicy.ReadOnly())
	})
}

func TestAnUnknownConstructIsRefusedByName(t *testing.T) {
	r := refusal(t, `SET statement_timeout = 0`, sqlpolicy.ReadOnly())
	if r.Subject == "" {
		t.Fatal("the refusal does not name the construct it refused")
	}
}

// The input and the recursion need a ceiling of their own, independent of
// other layers, because the text is attacker-chosen.
func TestTheCheckerProtectsItself(t *testing.T) {
	t.Run("a query too long to be a query", func(t *testing.T) {
		r := refusal(t, `SELECT `+strings.Repeat("1,", 40_000)+`1`, sqlpolicy.ReadOnly())
		if r.Code != sqlpolicy.CodeTooLong {
			t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeTooLong)
		}
	})

	t.Run("nesting deep enough to end the process", func(t *testing.T) {
		// A stack overflow would take the process down, not refuse the query.
		sql := "SELECT 1" + strings.Repeat(" FROM (SELECT 1", 200) + strings.Repeat(") q", 200)
		if err := checker.Check(sql, sqlpolicy.ReadOnly()); err == nil {
			t.Fatal("deep nesting was allowed")
		}
	})
}

func TestDecisionsWorthStating(t *testing.T) {
	t.Run("CREATE TABLE AS is refused", func(t *testing.T) {
		refusal(t, `CREATE TABLE notes AS SELECT * FROM suspects`, sqlpolicy.ReadOnly())
	})

	t.Run("a recursive CTE is allowed", func(t *testing.T) {
		// It can loop forever, but it is ordinary SQL; a runaway one holds one
		// execution slot until statement_timeout ends it.
		allow(t, `WITH RECURSIVE chain AS (
			SELECT id, boss_id FROM staff WHERE id = 1
			UNION ALL
			SELECT s.id, s.boss_id FROM staff s JOIN chain c ON s.boss_id = c.id
		) SELECT * FROM chain`, sqlpolicy.ReadOnly())
	})

	t.Run("VALUES on its own is allowed", func(t *testing.T) {
		allow(t, `VALUES (1, 'a'), (2, 'b')`, sqlpolicy.ReadOnly())
	})
}

// What follows the semicolon would be a syntax error inside a wrapping FROM.
func TestAnalyseReportsTheStatementsOwnText(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":            "SELECT 1",
		"SELECT 1;":           "SELECT 1",
		"SELECT 1; -- a note": "SELECT 1",
		// Leading whitespace and comments stay; only what follows the
		// semicolon is cut.
		"  \n SELECT 1  ":                    "  \n SELECT 1  ",
		"-- a note\nSELECT 1":                "-- a note\nSELECT 1",
		"/* thinking */ SELECT 1; \n\n":      "/* thinking */ SELECT 1",
		"SELECT 1 -- trailing, no semicolon": "SELECT 1 -- trailing, no semicolon",
	}
	for in, want := range cases {
		statement, err := checker.NewChecker().Analyse(in, sqlpolicy.ReadOnly())
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if statement.Text != want {
			t.Errorf("Analyse(%q).Text = %q, want %q", in, statement.Text, want)
		}
	}
}

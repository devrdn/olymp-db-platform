package checker_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

// writing is a contest that permits writing to one table and nothing else.
func writing() sqlpolicy.Policy { return sqlpolicy.ReadWrite("evidence") }

// notes permits a participant their own tables and views as well.
func notes() sqlpolicy.Policy {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowOwnTables = true
	p.AllowCreateView = true
	return p
}

func TestWritingReachesTheTablesThePolicyNames(t *testing.T) {
	for _, sql := range []string{
		`INSERT INTO evidence (note) VALUES ('a knife')`,
		`INSERT INTO public.evidence (note) VALUES ('a knife')`,
		`INSERT INTO evidence (note) SELECT name FROM suspects`,
		`UPDATE evidence SET note = 'edited' WHERE id = 1`,
		`DELETE FROM evidence WHERE id = 1`,
		`INSERT INTO evidence (note) VALUES ('x') RETURNING id`,
		`UPDATE evidence SET note = s.name FROM suspects s WHERE s.id = evidence.id`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, writing()) })
	}
}

// The table a policy does not name is the point of naming them.
func TestWritingStopsAtTheTablesThePolicyDoesNotName(t *testing.T) {
	for _, sql := range []string{
		`INSERT INTO suspects (name) VALUES ('someone')`,
		`UPDATE suspects SET city = 'nowhere'`,
		`DELETE FROM suspects`,
		`INSERT INTO other.evidence (note) VALUES ('x')`,
		`UPDATE pg_class SET relname = 'x'`,
	} {
		t.Run(sql, func(t *testing.T) {
			r := refusal(t, sql, writing())
			if r.Code != sqlpolicy.CodeTableNotWritable {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeTableNotWritable)
			}
		})
	}
}

// Everything that changes the shape of the contest stays refused however much
// writing is permitted: a participant may edit the evidence, never the case.
func TestWritingNeverReachesTheShapeOfTheContest(t *testing.T) {
	for _, sql := range []string{
		`DROP TABLE evidence`,
		`ALTER TABLE evidence ADD COLUMN planted text`,
		`CREATE TABLE public.mine (x int)`,
		`CREATE VIEW public.mine AS SELECT 1`,
		`CREATE INDEX ON evidence (note)`,
		`GRANT ALL ON evidence TO PUBLIC`,
		`CREATE TABLE mine (x int)`,
		`CREATE VIEW mine AS SELECT 1`,
	} {
		t.Run(sql, func(t *testing.T) { refusal(t, sql, notes()) })
	}
}

// Their own objects, in their own schema, and only when the contest says so.
func TestOwnObjectsNeedTheirOwnPermission(t *testing.T) {
	t.Run("allowed with the permission", func(t *testing.T) {
		for _, sql := range []string{
			`CREATE TABLE work.notes (id int, thought text)`,
			`CREATE TABLE work.notes AS SELECT * FROM suspects`,
			`DROP TABLE work.notes`,
			`CREATE VIEW work.clues AS SELECT * FROM evidence`,
			`CREATE OR REPLACE VIEW work.clues AS SELECT 1`,
			`DROP VIEW work.clues`,
			`INSERT INTO work.notes (thought) VALUES ('the butler')`,
			`SELECT * FROM work.notes`,
		} {
			t.Run(sql, func(t *testing.T) { allow(t, sql, notes()) })
		}
	})

	t.Run("refused without it", func(t *testing.T) {
		for _, sql := range []string{
			`CREATE TABLE work.notes (id int)`,
			`CREATE VIEW work.clues AS SELECT 1`,
			`DROP TABLE work.notes`,
		} {
			t.Run(sql, func(t *testing.T) { refusal(t, sql, writing()) })
		}
	})
}

func TestTemporaryTablesNeedTheirOwnPermission(t *testing.T) {
	temp := sqlpolicy.ReadWrite("evidence")
	temp.AllowTempTables = true

	allow(t, `CREATE TEMP TABLE scratch (x int)`, temp)
	refusal(t, `CREATE TEMP TABLE scratch (x int)`, writing())
	// A temporary table is not a licence to make a permanent one.
	refusal(t, `CREATE TABLE scratch (x int)`, temp)
}

// Permitting writing does not relax anything else: the walk still runs, the
// function list still applies, and the catalogues are still the installation's.
func TestPermittingWritingRelaxesNothingElse(t *testing.T) {
	for name, sql := range map[string]string{
		"a forbidden function in the values":  `INSERT INTO evidence (note) VALUES (pg_read_file('/etc/passwd'))`,
		"a forbidden function in RETURNING":   `INSERT INTO evidence (note) VALUES ('x') RETURNING pg_sleep(1)`,
		"a sensitive catalogue in the source": `INSERT INTO evidence (note) SELECT datname FROM pg_database`,
		"a sensitive catalogue in a WHERE":    `DELETE FROM evidence WHERE id IN (SELECT count(*) FROM pg_stat_activity)`,
		"two statements":                      `INSERT INTO evidence (note) VALUES ('x'); DROP TABLE suspects`,
		"a data-modifying CTE":                `WITH gone AS (DELETE FROM suspects RETURNING *) SELECT * FROM gone`,
		"an INSERT hidden in a CTE":           `WITH x AS (INSERT INTO suspects (name) VALUES ('y') RETURNING id) SELECT * FROM x`,
	} {
		t.Run(name, func(t *testing.T) { refusal(t, sql, notes()) })
	}
}

// Reading is unchanged by any of this, which is worth stating because the
// read-only path is the one every contest uses.
func TestReadingIsUnchangedByPermittingWriting(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM suspects`,
		`SELECT count(*) FROM evidence GROUP BY note`,
		`EXPLAIN SELECT * FROM suspects`,
		`SELECT relname FROM pg_class`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, notes()) })
	}
}

// A table definition is made of expressions, and an expression is where a
// forbidden function would hide if allowing column definitions had opened a
// door. It has not: they are walked like any other expression.
func TestATableDefinitionIsNotAWayPastTheFunctionList(t *testing.T) {
	for name, sql := range map[string]string{
		"in a default":      `CREATE TABLE work.notes (x text DEFAULT pg_read_file('/etc/passwd'))`,
		"in a check":        `CREATE TABLE work.notes (x int CHECK (pg_sleep(1) IS NULL))`,
		"in a create-as":    `CREATE TABLE work.notes AS SELECT pg_sleep(1)`,
		"in a view's query": `CREATE VIEW work.clues AS SELECT pg_ls_dir('/')`,
		"in an insert":      `INSERT INTO evidence (note) VALUES (pg_backend_pid()::text)`,
	} {
		t.Run(name, func(t *testing.T) {
			r := refusal(t, sql, notes())
			if r.Code != sqlpolicy.CodeFunctionNotSupported {
				t.Fatalf("code = %q, want %q (subject %q)", r.Code, sqlpolicy.CodeFunctionNotSupported, r.Subject)
			}
		})
	}
}

// And a foreign key is not a way to reach a table the contest did not open:
// referencing one is reading it, which every contest permits anyway, while
// writing to it is still refused by the target check.
func TestAConstraintDoesNotWidenWhatMayBeWritten(t *testing.T) {
	allow(t, `CREATE TABLE work.notes (id int REFERENCES suspects (id))`, notes())
	refusal(t, `INSERT INTO suspects (name) VALUES ('x')`, notes())
}

// Emptying a table the contest already opened for writing is permitted, and
// it is the only permitted statement that makes a database smaller rather
// than larger.
//
// No new authority: DELETE already empties the same table, and the shape of
// the contest is untouched — the table, its columns and its constraints are
// all still there afterwards. What TRUNCATE adds is that the pages go back to
// the database, which is what makes it a way out of a database at its size
// limit; an ordinary DELETE leaves them allocated, so a participant who
// filled their copy would be exactly as full afterwards.
func TestEmptyingAWritableTableIsPermitted(t *testing.T) {
	for _, sql := range []string{
		`TRUNCATE evidence`,
		`TRUNCATE TABLE evidence`,
		`TRUNCATE public.evidence`,
		`TRUNCATE evidence RESTART IDENTITY`,
		`TRUNCATE work.notes`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, notes()) })
	}
}

// And it stops exactly where every other write does.
func TestEmptyingStopsAtTheTablesThePolicyDoesNotName(t *testing.T) {
	for _, sql := range []string{
		`TRUNCATE suspects`,
		`TRUNCATE evidence, suspects`,
		`TRUNCATE other.evidence`,
	} {
		t.Run(sql, func(t *testing.T) {
			r := refusal(t, sql, notes())
			if r.Code != sqlpolicy.CodeTableNotWritable {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeTableNotWritable)
			}
		})
	}

	// Without the permission for their own tables, their own schema is no
	// different from anywhere else.
	t.Run("work without the permission", func(t *testing.T) {
		refusal(t, `TRUNCATE work.notes`, writing())
	})
}

// CASCADE is refused rather than followed. It reaches every table with a
// foreign key to the one named — a list the policy never described and the
// participant never wrote down — so a TRUNCATE stops at the table it names.
func TestEmptyingDoesNotCascade(t *testing.T) {
	r := refusal(t, `TRUNCATE evidence CASCADE`, notes())
	if r.Code != sqlpolicy.CodeNotPermitted {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeNotPermitted)
	}
}

// Statement.Frees is what the Query Runner turns on at the disk quota, so
// which statements set it is worth asserting on its own: a write that can
// only make the database smaller, and nothing else.
func TestTheCheckerSaysWhichStatementsCanOnlyFreeSpace(t *testing.T) {
	for sql, want := range map[string]bool{
		`TRUNCATE evidence`:     true,
		`TRUNCATE work.notes`:   true,
		`DROP TABLE work.notes`: true,
		`DROP VIEW work.clues`:  true,

		`INSERT INTO evidence (note) VALUES ('x')`: false,
		`UPDATE evidence SET note = 'x'`:           false,
		// The one that reads like a way out and is not: the rows go, the
		// pages stay, and pg_database_size does not move.
		`DELETE FROM evidence`:            false,
		`CREATE TABLE work.notes (x int)`: false,
		`SELECT * FROM evidence`:          false,
	} {
		t.Run(sql, func(t *testing.T) {
			statement, err := checker.NewChecker().Analyse(sql, notes())
			if err != nil {
				t.Fatalf("refused a legitimate query: %v", err)
			}
			if statement.Frees != want {
				t.Fatalf("Frees = %v, want %v", statement.Frees, want)
			}
		})
	}
}

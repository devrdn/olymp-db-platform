package checker_test

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

// None of these is named by a rule; they are refused by being absent from the
// allow-list.
func TestTheFunctionsThatAreNotOnTheList(t *testing.T) {
	for _, sql := range []string{
		// Holding an execution slot for the whole timeout.
		`SELECT pg_sleep(10)`,
		`SELECT pg_sleep_for('5 minutes')`,
		// Reading the host.
		`SELECT pg_read_file('/etc/passwd')`,
		`SELECT pg_read_binary_file('/etc/passwd')`,
		`SELECT pg_ls_dir('/')`,
		`SELECT pg_stat_file('/etc/passwd')`,
		`SELECT lo_import('/etc/passwd')`,
		`SELECT lo_export(1, '/tmp/out')`,
		// Reaching another server, out of the sandbox.
		`SELECT * FROM dblink('dbname=core', 'SELECT 1') AS t(x int)`,
		`SELECT dblink_connect('dbname=core')`,
		// Interfering with the installation or with other participants.
		`SELECT pg_terminate_backend(1)`,
		`SELECT pg_cancel_backend(1)`,
		`SELECT set_config('statement_timeout', '0', false)`,
		`SELECT current_setting('data_directory')`,
		`SELECT pg_backend_pid()`,
		`SELECT version()`,
		`SELECT current_user`,
		`SELECT inet_server_addr()`,
		`SELECT pg_database_size('postgres')`,
		`SELECT query_to_xml('SELECT 1', true, false, '')`,
	} {
		t.Run(sql, func(t *testing.T) {
			err := checker.Check(sql, sqlpolicy.ReadOnly())
			if err == nil {
				t.Fatalf("allowed: %s", sql)
			}
		})
	}
}

// The operator needs the name to decide whether it belongs on the list.
func TestARefusalNamesTheFunction(t *testing.T) {
	r := refusal(t, `SELECT pg_sleep(1)`, sqlpolicy.ReadOnly())
	if r.Code != sqlpolicy.CodeFunctionNotSupported {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeFunctionNotSupported)
	}
	if r.Subject != "pg_sleep" {
		t.Fatalf("subject = %q, want pg_sleep", r.Subject)
	}
}

func TestAFunctionIsFoundWhereverItHides(t *testing.T) {
	for name, sql := range map[string]string{
		"in a WHERE":            `SELECT 1 FROM t WHERE pg_sleep(1) IS NOT NULL`,
		"in a subquery":         `SELECT (SELECT pg_sleep(1))`,
		"in a CTE":              `WITH slow AS (SELECT pg_sleep(1) x) SELECT * FROM slow`,
		"behind a cast":         `SELECT pg_sleep(1)::text`,
		"in ORDER BY":           `SELECT a FROM t ORDER BY pg_sleep(1)`,
		"in HAVING":             `SELECT count(*) FROM t GROUP BY a HAVING pg_sleep(1) IS NULL`,
		"in a CASE":             `SELECT CASE WHEN true THEN pg_sleep(1) END`,
		"inside another call":   `SELECT upper(pg_read_file('/etc/passwd'))`,
		"in a window frame":     `SELECT sum(a) OVER (ORDER BY pg_sleep(1)) FROM t`,
		"in a join condition":   `SELECT 1 FROM a JOIN b ON pg_sleep(1) IS NULL`,
		"in a FROM clause":      `SELECT * FROM generate_series(1, 2), pg_ls_dir('/')`,
		"under seven levels":    `SELECT (SELECT (SELECT (SELECT (SELECT (SELECT (SELECT pg_sleep(1)))))))`,
		"in an array":           `SELECT ARRAY[pg_sleep(1)]`,
		"as an aggregate's arg": `SELECT count(pg_sleep(1)) FROM t`,
	} {
		t.Run(name, func(t *testing.T) {
			// Assert the code, so a refusal for the wrong reason fails.
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Code != sqlpolicy.CodeFunctionNotSupported {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeFunctionNotSupported)
			}
		})
	}
}

func TestNameFoldingAndQualification(t *testing.T) {
	t.Run("case does not hide a function", func(t *testing.T) {
		for _, sql := range []string{`SELECT PG_SLEEP(1)`, `SELECT Pg_Sleep(1)`, `SELECT "pg_sleep"(1)`} {
			refusal(t, sql, sqlpolicy.ReadOnly())
		}
	})

	t.Run("nor does spelling out pg_catalog", func(t *testing.T) {
		refusal(t, `SELECT pg_catalog.pg_sleep(1)`, sqlpolicy.ReadOnly())
	})

	t.Run("pg_catalog on a permitted function is the same function", func(t *testing.T) {
		allow(t, `SELECT pg_catalog.upper(name) FROM suspects`, sqlpolicy.ReadOnly())
	})

	t.Run("any other qualification is somebody's own function", func(t *testing.T) {
		// Reducing to the last part would let `evil.upper` through as `upper`.
		for _, sql := range []string{
			`SELECT public.upper(name) FROM suspects`,
			`SELECT work.helper(1)`,
			`SELECT a.b.c(1)`,
		} {
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Code != sqlpolicy.CodeFunctionNotSupported {
				t.Fatalf("%s: code = %q", sql, r.Code)
			}
			if !strings.Contains(r.Subject, ".") {
				t.Fatalf("%s: the refusal dropped the qualification: %q", sql, r.Subject)
			}
		}
	})
}

func TestAnOperatorCanExtendTheList(t *testing.T) {
	const sql = `SELECT soundex(name) FROM suspects`

	if err := checker.Check(sql, sqlpolicy.ReadOnly()); err == nil {
		t.Fatal("soundex is allowed by default; pick a function that is not")
	}
	if err := checker.NewChecker("soundex").Check(sql, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("an extended checker still refused it: %v", err)
	}
	// Extending one checker must not quietly extend everyone's.
	if err := checker.Check(sql, sqlpolicy.ReadOnly()); err == nil {
		t.Fatal("extending a checker changed the standard one")
	}
}

// The times belong to the game; CURRENT_USER and CURRENT_CATALOG name the
// role and the database, which identify the contest and participant.
func TestTheValueFunctionsSplitTwoWays(t *testing.T) {
	for _, sql := range []string{
		`SELECT current_date`, `SELECT current_time`, `SELECT current_timestamp`,
		`SELECT localtime`, `SELECT localtimestamp`,
	} {
		t.Run("allows "+sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}

	for _, sql := range []string{
		`SELECT current_user`, `SELECT session_user`, `SELECT user`,
		`SELECT current_role`, `SELECT current_catalog`, `SELECT current_schema`,
	} {
		t.Run("refuses "+sql, func(t *testing.T) {
			r := refusal(t, sql, sqlpolicy.ReadOnly())
			if r.Subject == "" {
				t.Fatal("the refusal does not name what it refused")
			}
		})
	}
}

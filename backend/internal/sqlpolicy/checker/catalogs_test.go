package checker_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// Reading the shape of the data is part of the exercise: a detective opens the
// filing cabinet before opening a drawer.
func TestTheStructuralCatalogsAreReadableByDefault(t *testing.T) {
	for _, sql := range []string{
		`SELECT relname FROM pg_class`,
		`SELECT * FROM pg_catalog.pg_class`,
		`SELECT attname FROM pg_attribute`,
		`SELECT table_name FROM information_schema.tables`,
		`SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'suspects'`,
		`SELECT * FROM pg_indexes`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

func TestAContestMayCloseTheCatalogs(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	r := refusal(t, `SELECT relname FROM pg_class`, closed)
	if r.Code != sqlpolicy.CodeCatalogNotAllowed {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeCatalogNotAllowed)
	}
	// The game's own tables are unaffected: the flag closes the catalog, not
	// the contest.
	allow(t, `SELECT * FROM suspects`, closed)
}

// These describe the installation and the other participants, not the game.
// pg_stat_activity in particular shows the queries everyone else is running,
// which during an olympiad is simply the answers.
func TestTheSensitiveCatalogsAreNeverReadable(t *testing.T) {
	open := sqlpolicy.ReadOnly() // AllowCatalog is true — deliberately.

	for _, sql := range []string{
		`SELECT datname FROM pg_database`,
		`SELECT * FROM pg_catalog.pg_database`,
		`SELECT query FROM pg_stat_activity`,
		`SELECT * FROM pg_roles`,
		`SELECT * FROM pg_user`,
		`SELECT * FROM pg_shadow`,
		`SELECT * FROM pg_authid`,
		`SELECT * FROM pg_settings`,
	} {
		t.Run(sql, func(t *testing.T) {
			r := refusal(t, sql, open)
			if r.Code != sqlpolicy.CodeCatalogNotReadable {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeCatalogNotReadable)
			}
		})
	}
}

// Refusing only the qualified spelling would be a check anyone gets past by
// deleting eleven characters, and refusing only the top level would be one
// anyone gets past by writing a join.
func TestASensitiveCatalogIsFoundWhereverItIsJoined(t *testing.T) {
	for name, sql := range map[string]string{
		"in a join":      `SELECT s.name FROM suspects s JOIN pg_database d ON true`,
		"in a subquery":  `SELECT (SELECT count(*) FROM pg_stat_activity)`,
		"in a CTE":       `WITH who AS (SELECT * FROM pg_roles) SELECT * FROM who`,
		"in a UNION":     `SELECT name FROM suspects UNION SELECT datname FROM pg_database`,
		"in an EXPLAIN":  `EXPLAIN SELECT * FROM pg_stat_activity`,
		"in a FROM list": `SELECT * FROM suspects, pg_settings`,
	} {
		t.Run(name, func(t *testing.T) { refusal(t, sql, sqlpolicy.ReadOnly()) })
	}
}

// Permitting writing must not quietly permit reading the installation: the
// catalogue rules are not part of what a mode relaxes.
func TestTheWritingModeDoesNotOpenTheCatalogues(t *testing.T) {
	r := refusal(t, `SELECT datname FROM pg_database`, sqlpolicy.ReadWrite("evidence"))
	if r.Code != sqlpolicy.CodeCatalogNotReadable {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeCatalogNotReadable)
	}
}

// A policy nobody filled in must decide nothing at all.
func TestAnIncoherentPolicyRefusesEverything(t *testing.T) {
	r := refusal(t, `SELECT 1`, sqlpolicy.Policy{})
	if r.Code != sqlpolicy.CodeInvalidPolicy {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeInvalidPolicy)
	}
}

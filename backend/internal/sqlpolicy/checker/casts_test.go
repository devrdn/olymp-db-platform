package checker_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// A cast to a reg* pseudo-type resolves a name to its OID by looking it up in
// the catalog — an existence oracle with none of a SELECT's vocabulary for the
// catalog rules to catch. Refused even in a contest that opened the
// structural catalogs, and even one that closed them specifically so the
// schema would have to be discovered by playing.
func TestARegTypeCastIsRefusedWhateverItNames(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	for name, sql := range map[string]string{
		"a table, unqualified":      `SELECT 'evidence'::regclass`,
		"a table, schema-qualified": `SELECT 'public.evidence'::regclass`,
		"a role by oid":             `SELECT 10::regrole`,
		"a namespace":               `SELECT 'pg_catalog'::regnamespace`,
		"a function signature":      `SELECT 'upper(text)'::regprocedure`,
	} {
		t.Run(name, func(t *testing.T) {
			r := refusal(t, sql, closed)
			if r.Code != sqlpolicy.CodeCatalogNotReadable {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeCatalogNotReadable)
			}
		})
	}
}

// The whole deny-list, not just the four the audit exercised, and spelled
// both ways: PostgreSQL accepts `regclass` and `pg_catalog.regclass` for the
// same type, and a check catching only one is a check anyone gets past by
// adding or removing eleven characters.
func TestEveryRegTypeIsOnTheDenyList(t *testing.T) {
	open := sqlpolicy.ReadOnly() // AllowCatalog is true — deliberately: this
	// must be refused regardless.

	for _, regType := range []string{
		"regclass", "regproc", "regprocedure", "regoper", "regoperator",
		"regtype", "regrole", "regnamespace", "regconfig", "regdictionary",
		"regcollation",
	} {
		t.Run(regType, func(t *testing.T) {
			refusal(t, `SELECT 1::`+regType, open)
		})
		t.Run(regType+" qualified", func(t *testing.T) {
			refusal(t, `SELECT 1::pg_catalog.`+regType, open)
		})
	}
}

// A cast nested inside an otherwise ordinary query is still a cast: the
// checker walks the whole tree, not only the top of it.
func TestARegTypeCastIsFoundWhereverItIsNested(t *testing.T) {
	for name, sql := range map[string]string{
		"in a WHERE":       `SELECT name FROM suspects WHERE id = 'evidence'::regclass::oid`,
		"in a subquery":    `SELECT (SELECT 'evidence'::regclass)`,
		"in a CTE":         `WITH x AS (SELECT 'evidence'::regclass) SELECT * FROM x`,
		"as a nested cast": `SELECT 'evidence'::regclass::text`,
	} {
		t.Run(name, func(t *testing.T) { refusal(t, sql, sqlpolicy.ReadOnly()) })
	}
}

// Casts that are not to a reg* type are ordinary value conversions and must
// keep working: a fix here that refused every cast would be as useless as one
// that refused none of them.
func TestAnOrdinaryCastStillPasses(t *testing.T) {
	for _, sql := range []string{
		`SELECT '42'::int`,
		`SELECT now()::date`,
		`SELECT age::text FROM suspects`,
		`SELECT '3.14'::numeric(10,2)`,
	} {
		t.Run(sql, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

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

// A reg* pseudo-type is a name to look up in the catalog whether it names a
// value's type or a column's — and a column definition is where the deny-list
// used to have nothing to say. A regrole column filled through an ordinary
// ::oid cast and read back is the same existence oracle as the cast form,
// just paid for in bulk instead of one row at a time, and allow_catalog makes
// no difference to it because ClassifyRelation never sees a type name.
func TestARegTypeColumnIsRefusedWhateverItNames(t *testing.T) {
	closed := sqlpolicy.ReadWrite("evidence")
	closed.AllowOwnTables = true
	closed.AllowCatalog = false

	for _, regType := range []string{"regrole", "regclass", "regprocedure", "regnamespace"} {
		t.Run(regType, func(t *testing.T) {
			r := refusal(t, `CREATE TABLE work.r (c `+regType+`)`, closed)
			if r.Code != sqlpolicy.CodeCatalogNotReadable {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeCatalogNotReadable)
			}
		})
	}
}

// The attack the review found: fill the column through an ordinary cast
// (::oid is not itself a catalog lookup), then read it back — regrole's own
// output function is what turns the OID into a role name, not anything the
// checker sees as a cast. The column definition is refused before either
// statement matters, which is where this has to be caught: by the time a row
// exists, the checker is not in the loop.
func TestARegTypeColumnIsRefusedEvenFilledThroughAnOrdinaryCast(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowOwnTables = true

	refusal(t, `CREATE TABLE work.r (c regrole)`, p)
}

// A temporary table is a separate permission from a participant's own tables,
// but the same grammar shape, and the same escape route: it is checked here
// too.
func TestARegTypeColumnIsRefusedInATemporaryTableToo(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowTempTables = true

	refusal(t, `CREATE TEMP TABLE t (c regrole)`, p)
}

// Ordinary column types must keep working: a fix that refused every column
// definition would close the oracle by making CREATE TABLE useless.
func TestOrdinaryColumnTypesStillPass(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowOwnTables = true

	allow(t, `CREATE TABLE work.notes (a int, b text, c numeric(10,2), d timestamptz, e int[])`, p)
}

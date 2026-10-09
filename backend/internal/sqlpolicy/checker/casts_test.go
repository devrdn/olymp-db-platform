package checker_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// A reg* cast is a catalog existence oracle, refused whatever AllowCatalog says.
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

// Every reg* type, bare and pg_catalog-qualified.
func TestEveryRegTypeIsOnTheDenyList(t *testing.T) {
	open := sqlpolicy.ReadOnly() // AllowCatalog is true.

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

// A reg* column filled through ::oid and read back is the same oracle as the
// cast, in bulk.
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

// regrole's output function turns the stored OID into a role name, which the
// checker never sees, so the column definition is where it must be refused.
func TestARegTypeColumnIsRefusedEvenFilledThroughAnOrdinaryCast(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowOwnTables = true

	refusal(t, `CREATE TABLE work.r (c regrole)`, p)
}

func TestARegTypeColumnIsRefusedInATemporaryTableToo(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowTempTables = true

	refusal(t, `CREATE TEMP TABLE t (c regrole)`, p)
}

func TestOrdinaryColumnTypesStillPass(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowOwnTables = true

	allow(t, `CREATE TABLE work.notes (a int, b text, c numeric(10,2), d timestamptz, e int[])`, p)
}

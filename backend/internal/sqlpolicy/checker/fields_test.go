package checker_test

import (
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy/checker"
)

// everythingOn is the widest policy a contest can choose.
func everythingOn() sqlpolicy.Policy {
	p := sqlpolicy.ReadWrite("evidence")
	p.AllowCreateView = true
	p.AllowOwnTables = true
	p.AllowTempTables = true
	return p
}

// PostgreSQL runs each of these as pg_sleep(…), with no function call in the
// parse tree. No policy option is about functions, so both ends of the range
// must refuse them.
func TestFieldSelectionCannotStandInForACall(t *testing.T) {
	for name, sql := range map[string]string{
		"from a constant":                  `SELECT (1).pg_sleep`,
		"from a subquery's column":         `SELECT (q.x).pg_sleep FROM (SELECT 1 AS x) q`,
		"from a function's alias":          `SELECT g.pg_sleep FROM generate_series(1, 1) g`,
		"from a function with no alias":    `SELECT generate_series.pg_sleep FROM generate_series(1, 1)`,
		"beside the columns it declares":   `SELECT g.pg_sleep FROM generate_series(1, 1) AS g(n)`,
		"named like the alias":             `SELECT current_setting.current_setting FROM json_array_elements_text('["port"]') AS current_setting`,
		"in a WHERE":                       `SELECT 1 FROM generate_series(1, 1) g WHERE g.pg_sleep IS NULL`,
		"from a subquery under the alias":  `SELECT (SELECT g.pg_sleep) FROM generate_series(1, 1) g`,
		"from a function inside a join":    `SELECT g.pg_sleep FROM suspects s JOIN generate_series(1, 1) g ON true`,
		"from an expression used as a set": `SELECT "coalesce".pg_sleep FROM coalesce(1)`,
		// Another function under the same alias, in a scope of its own, does
		// not make the field a column of this one.
		"declared by a namesake in a subquery": `SELECT g.pg_sleep FROM generate_series(1, 1) g
			WHERE EXISTS (SELECT 1 FROM unnest(ARRAY[1]) AS g(pg_sleep))`,
		"declared by a namesake in a UNION arm": `SELECT g.pg_sleep FROM generate_series(1, 1) g
			UNION ALL SELECT 1 FROM unnest(ARRAY[1]) AS g(pg_sleep)`,
		"declared by a namesake in an unused CTE": `WITH spare AS (SELECT 1 FROM unnest(ARRAY[1]) AS g(pg_sleep))
			SELECT g.pg_sleep FROM generate_series(1, 1) g`,
	} {
		for policyName, policy := range map[string]sqlpolicy.Policy{
			"read-only":     sqlpolicy.ReadOnly(),
			"everything on": everythingOn(),
		} {
			t.Run(name+"/"+policyName, func(t *testing.T) {
				r := refusal(t, sql, policy)
				if r.Code != sqlpolicy.CodeConstructNotSupported {
					t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeConstructNotSupported)
				}
			})
		}
	}
}

func TestFieldSelectionIsRefusedInAWriteToo(t *testing.T) {
	for name, sql := range map[string]string{
		"an UPDATE's FROM":  `UPDATE evidence SET note = 'x' FROM generate_series(1, 1) g WHERE g.pg_sleep IS NULL`,
		"a DELETE's USING":  `DELETE FROM evidence USING generate_series(1, 1) g WHERE g.pg_sleep IS NULL`,
		"an INSERT's value": `INSERT INTO evidence (note) VALUES ((1).pg_sleep)`,
	} {
		t.Run(name, func(t *testing.T) {
			r := refusal(t, sql, everythingOn())
			if r.Code != sqlpolicy.CodeConstructNotSupported {
				t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeConstructNotSupported)
			}
		})
	}
}

// The participant has to find the spelling in their own query.
func TestAFieldSelectionRefusalQuotesWhatWasWritten(t *testing.T) {
	for sql, want := range map[string]string{
		`SELECT (1).pg_sleep`:                            "field selection .pg_sleep",
		`SELECT g.pg_sleep FROM generate_series(1, 1) g`: "field selection g.pg_sleep",
	} {
		if r := refusal(t, sql, sqlpolicy.ReadOnly()); r.Subject != want {
			t.Errorf("%s: subject = %q, want %q", sql, r.Subject, want)
		}
	}
}

func TestWhatLooksLikeFieldSelectionAndIsNot(t *testing.T) {
	for name, sql := range map[string]string{
		"a subscript":                         `SELECT (tags)[1] FROM suspects`,
		"a subscript of an array constructor": `SELECT (ARRAY[1, 2])[1]`,
		"a slice":                             `SELECT (tags)[1:2] FROM suspects`,
		"every field of a row":                `SELECT (s).* FROM suspects s`,
		"a table's column":                    `SELECT s.name FROM suspects s`,
		"a schema-qualified column":           `SELECT public.suspects.name FROM public.suspects`,
		"a subquery's column":                 `SELECT q.name FROM (SELECT name FROM suspects) q`,
		"a column the alias declares":         `SELECT t.x FROM unnest(ARRAY[1, 2]) AS t(x)`,
		"both columns the alias declares":     `SELECT t.x, t.n FROM unnest(ARRAY[1, 2]) WITH ORDINALITY AS t(x, n)`,
		"every column of a function":          `SELECT g.* FROM generate_series(1, 2) g`,
		"a function's alias on its own":       `SELECT g FROM generate_series(1, 2) g`,
		"a function's column unqualified":     `SELECT key, value FROM json_each('{"a": 1}') e`,
		"an expression used as a set, named":  `SELECT c FROM coalesce(1) AS c`,
		"a column two namesakes both declare": `SELECT t.x FROM unnest(ARRAY[1]) AS t(x) UNION ALL SELECT t.x FROM unnest(ARRAY[2]) AS t(x, n)`,
	} {
		for policyName, policy := range map[string]sqlpolicy.Policy{
			"read-only":     sqlpolicy.ReadOnly(),
			"everything on": everythingOn(),
		} {
			t.Run(name+"/"+policyName, func(t *testing.T) { allow(t, sql, policy) })
		}
	}
}

// A function that returns a record names its columns in a definition list,
// not in the alias.
func TestAColumnDefinitionListDeclaresColumnsToo(t *testing.T) {
	records := checker.NewChecker("json_to_record")

	if err := records.Check(`SELECT r.a FROM json_to_record('{"a": 1}') AS r(a int)`, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("refused a declared column: %v", err)
	}
	if err := records.Check(`SELECT r.pg_sleep FROM json_to_record('{"a": 1}') AS r(a int)`, sqlpolicy.ReadOnly()); err == nil {
		t.Fatal("allowed a field the definition list does not declare")
	}
}

package checker_test

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// The parser overflows its C stack at about 25,000 chained operators, inside
// the byte cap, and the overflow is a crash no Go code can catch. Without the
// guard this test does not fail: it kills the test binary.
func TestAnOperatorChainIsRefusedBeforeTheParserSeesIt(t *testing.T) {
	sql := "SELECT 1" + strings.Repeat("+1", 30_000)
	if len(sql) > sqlpolicy.MaxQueryBytes {
		t.Fatalf("the chain must fit the byte cap, or the cap refuses it and this proves nothing: %d bytes", len(sql))
	}

	r := refusal(t, sql, sqlpolicy.ReadOnly())
	if r.Code != sqlpolicy.CodeTooDeep {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeTooDeep)
	}
}

func TestBracketNestingBeyondTheBoundIsRefused(t *testing.T) {
	sql := "SELECT " + strings.Repeat("(", 600) + "1" + strings.Repeat(")", 600)

	r := refusal(t, sql, sqlpolicy.ReadOnly())
	if r.Code != sqlpolicy.CodeTooDeep {
		t.Fatalf("code = %q, want %q", r.Code, sqlpolicy.CodeTooDeep)
	}
}

// What the parser never builds a tree from must not count: a long constant
// full of operator characters is one node.
func TestOperatorsInsideConstantsAndCommentsDoNotCount(t *testing.T) {
	plus := strings.Repeat("+", 10_000)
	for name, sql := range map[string]string{
		"string constant":       `SELECT '` + plus + `'`,
		"escaped quote inside":  `SELECT 'it''s ` + plus + `'`,
		"dollar-quoted":         `SELECT $$` + plus + `$$`,
		"tagged dollar-quoted":  `SELECT $q$` + plus + `$q$`,
		"line comment":          "SELECT 1 -- " + plus,
		"block comment":         `SELECT 1 /* ` + plus + ` */`,
		"nested block comment":  `SELECT 1 /* a /* ` + plus + ` */ b */`,
		"apostrophe in comment": "SELECT 1 -- it's " + plus,
	} {
		t.Run(name, func(t *testing.T) { allow(t, sql, sqlpolicy.ReadOnly()) })
	}
}

// The bound is generous: a query with a few hundred operators is unusual but
// legitimate, and the AND/OR the parser flattens are not operators here.
func TestAWideButShallowQueryIsStillAllowed(t *testing.T) {
	conditions := make([]string, 300)
	for i := range conditions {
		conditions[i] = "1 = 1"
	}
	allow(t, "SELECT 1 WHERE "+strings.Join(conditions, " OR "), sqlpolicy.ReadOnly())
}

package checker

import (
	"testing"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// Every name in the allow-list must be a node the grammar can actually
// produce.
//
// A misspelt entry is the worst kind of mistake this package can make: it
// allows nothing, refuses a construct that was meant to be permitted, and says
// so only to whichever participant happens to write that query during a
// contest. `sql_value_function` for `sqlvalue_function` cost exactly that —
// `SELECT now()` was refused — and no behavioural test noticed, because a test
// only covers the queries somebody thought to write down.
//
// The names come from the parse tree's own oneof, so this compares the list
// against the parser rather than against another list.
func TestEveryAllowedKindIsRealGrammar(t *testing.T) {
	oneof := (&pg.Node{}).ProtoReflect().Descriptor().Oneofs().ByName("node")
	if oneof == nil {
		t.Fatal("the Node message has no `node` oneof")
	}

	real := make(map[string]struct{}, oneof.Fields().Len())
	for i := range oneof.Fields().Len() {
		real[string(oneof.Fields().Get(i).Name())] = struct{}{}
	}

	for kind := range allowedKinds {
		if _, ok := real[kind]; !ok {
			t.Errorf("%q is not a node the parser produces — a typo allows nothing", kind)
		}
	}
}

// The statements that write must not be reachable as nodes, at any depth.
// This is the assertion that refuses a data-modifying CTE, and stating it
// against the list directly keeps it true even if the walk is rewritten.
func TestNoStatementThatWritesIsAllowed(t *testing.T) {
	for _, kind := range []string{
		"insert_stmt", "update_stmt", "delete_stmt", "merge_stmt",
		"create_stmt", "create_table_as_stmt", "drop_stmt", "alter_table_stmt",
		"truncate_stmt", "view_stmt", "index_stmt", "copy_stmt",
		"variable_set_stmt", "do_stmt", "call_stmt", "grant_stmt",
		"create_role_stmt", "transaction_stmt", "execute_stmt", "prepare_stmt",
		"create_function_stmt", "vacuum_stmt", "lock_stmt", "explain_stmt",
	} {
		if _, allowed := allowedKinds[kind]; allowed {
			t.Errorf("%q is in the allow-list; a CTE could then carry it", kind)
		}
	}
}

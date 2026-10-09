package checker

import (
	"testing"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// A misspelt entry silently refuses a construct meant to be allowed, which
// behavioural tests only catch for queries somebody wrote down.
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

// This is what refuses a data-modifying CTE, asserted on the list itself so
// it holds even if the walk is rewritten.
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

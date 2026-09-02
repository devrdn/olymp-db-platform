package sqlpolicy

// The node types a query may be built from.
//
// This is the list the whole package turns on. Every node in the parse tree is
// looked up here, and anything absent is refused — not because it is known to
// be dangerous, but because it is not known at all. `COPY`, `SET`, `DO`,
// `CREATE ROLE`, and every construct a future PostgreSQL release adds are
// refused by saying nothing about them, which is the only way a list stays
// correct without being maintained against a moving target.
//
// The names are the grammar's own, read off the parse tree (kindOf), so they
// cannot drift from what the parser actually produces the way a hand-kept
// mapping would.
//
// Statements are conspicuous by their absence: `select_stmt` is here because a
// subquery is a select, but there is no `insert_stmt`, `delete_stmt` or
// `create_stmt`. That is what refuses a data-modifying CTE — the write sits
// several levels inside a tree whose root is an honest SELECT, and the root
// check cannot see it.
var allowedKinds = names(
	// The shape of a query.
	"select_stmt", "with_clause", "common_table_expr", "join_expr",
	"range_var", "range_subselect", "range_function", "alias", "res_target",
	"sort_by", "window_def", "grouping_set",

	// Expressions.
	"a_expr", "bool_expr", "column_ref", "a_const", "a_star", "a_array_expr",
	"a_indirection", "a_indices", "case_expr", "case_when", "coalesce_expr",
	"min_max_expr", "null_test", "boolean_test", "row_expr", "type_cast",
	"type_name", "collate_clause", "param_ref", "named_arg_expr", "sub_link",
	"func_call", "sqlvalue_function", "grouping_func", "list",

	// Leaves the grammar spells as their own nodes.
	"integer", "float", "boolean", "string", "bit_string",
)

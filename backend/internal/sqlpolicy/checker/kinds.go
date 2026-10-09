package checker

// allowedKinds are the node types a query may be built from. Every node in
// the tree is looked up here and anything absent is refused, so COPY, SET,
// DO, CREATE ROLE and whatever a future release adds are refused unlisted.
//
// No write statement is listed (only select_stmt, for subqueries); that is
// what refuses a data-modifying CTE under a SELECT root.
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

	// Table definitions, reachable only under a CREATE TABLE the root check
	// permitted. Defaults and checks inside are walked like any expression.
	"column_def", "constraint",
)

package checker

import "strings"

// The functions a participant may call, by name.
//
// An allow-list, and the reasoning is the same as everywhere else in this
// package: a deny-list of dangerous functions is a list somebody has to keep
// complete, against an extension surface that grows with every PostgreSQL
// release. `pg_sleep`, `pg_read_file`, `dblink`, `lo_import` and the rest are
// not absent from here because they were considered and rejected — they are
// absent because nothing was added that was not needed.
//
// The cost is real and is paid deliberately: a legitimate function nobody
// thought of is refused, and the participant sees a refusal for a query that
// is not their mistake. That is why the refusal names the function, why the
// journal panel aggregates these refusals (section 9.1), and why the list is
// extendable at run time by an operator rather than only by a release
// (NewChecker). Section 14 makes replenishing it after the pilot an explicit
// step of stage 7.
//
// Note what is *not* here and does not need to be: `coalesce`, `nullif`,
// `greatest`, `least`, `current_date` and casts are not function calls in the
// parse tree at all — the grammar gives each its own node type, checked as a
// construct rather than as a name. `now()` is not among them: it looks like
// those but parses as an ordinary call, so it has to be listed.
var defaultFunctions = names(
	// Arithmetic.
	"abs", "cbrt", "ceil", "ceiling", "degrees", "div", "exp", "floor", "gcd",
	"lcm", "ln", "log", "log10", "mod", "pi", "power", "radians", "random",
	"round", "sign", "sqrt", "trunc", "width_bucket",
	"acos", "asin", "atan", "atan2", "cos", "cot", "sin", "tan",

	// Strings.
	"ascii", "btrim", "char_length", "character_length", "chr", "concat",
	"concat_ws", "decode", "encode", "format", "initcap", "left", "length",
	"lower", "lpad", "ltrim", "md5", "octet_length", "overlay", "position",
	"repeat", "replace", "reverse", "right", "rpad", "rtrim", "split_part",
	"starts_with", "strpos", "substr", "substring", "translate", "trim",
	"upper",
	"regexp_match", "regexp_matches", "regexp_replace", "regexp_split_to_array",
	"regexp_split_to_table", "similar_to_escape",

	// Dates and times.
	"age", "clock_timestamp", "date_part", "date_trunc", "extract", "isfinite",
	"justify_days", "justify_hours", "justify_interval", "make_date",
	"make_interval", "make_time", "make_timestamp", "now",
	"statement_timestamp", "timezone", "to_char", "to_date", "to_number",
	"to_timestamp", "transaction_timestamp",

	// Aggregates.
	"array_agg", "avg", "bit_and", "bit_or", "bool_and", "bool_or", "corr",
	"count", "covar_pop", "covar_samp", "every", "json_agg", "json_object_agg",
	"jsonb_agg", "jsonb_object_agg", "max", "min", "mode", "percentile_cont",
	"percentile_disc", "stddev", "stddev_pop", "stddev_samp", "string_agg",
	"sum", "var_pop", "var_samp", "variance",

	// Window functions.
	"cume_dist", "dense_rank", "first_value", "lag", "last_value", "lead",
	"nth_value", "ntile", "percent_rank", "rank", "row_number",

	// Arrays.
	"array_append", "array_cat", "array_dims", "array_length", "array_lower",
	"array_ndims", "array_position", "array_positions", "array_prepend",
	"array_remove", "array_replace", "array_to_string", "array_upper",
	"cardinality", "string_to_array", "unnest",

	// JSON.
	"json_array_elements", "json_array_elements_text", "json_array_length",
	"json_build_array", "json_build_object", "json_each", "json_each_text",
	"json_extract_path", "json_extract_path_text", "json_object_keys",
	"json_typeof", "jsonb_array_elements", "jsonb_array_elements_text",
	"jsonb_array_length", "jsonb_build_array", "jsonb_build_object",
	"jsonb_each", "jsonb_each_text", "jsonb_extract_path",
	"jsonb_extract_path_text", "jsonb_object_keys", "jsonb_pretty",
	"jsonb_typeof", "to_json", "to_jsonb",

	// Generating rows.
	"generate_series", "generate_subscripts",
)

// names builds a lookup from a list written for reading.
func names(list ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(list))
	for _, name := range list {
		set[name] = struct{}{}
	}
	return set
}

// functionName reduces a parsed function name to the one to look up, and
// reports whether the call is addressable at all.
//
// A call may be written qualified. `pg_catalog.upper(x)` is the same function
// as `upper(x)`, so the schema is dropped — but only for pg_catalog and the
// default schema. A call qualified with anything else is reaching for a
// function somebody installed, which is by definition not on a list of
// standard ones, and is refused as written rather than silently reduced to
// its last element.
func functionName(parts []string) (string, bool) {
	switch len(parts) {
	case 1:
		return strings.ToLower(parts[0]), true
	case 2:
		if strings.ToLower(parts[0]) == "pg_catalog" {
			return strings.ToLower(parts[1]), true
		}
		return strings.ToLower(strings.Join(parts, ".")), false
	default:
		return strings.ToLower(strings.Join(parts, ".")), false
	}
}

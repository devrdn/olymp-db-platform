/**
 * How a name is written into SQL so that PostgreSQL reads it back as the same
 * table or column.
 */

/**
 * Every PostgreSQL keyword that is not "unreserved" (reserved, column-name
 * and type-or-function-name), from `src/include/parser/kwlist.h`. Such a word
 * is refused or misread as a bare table name, so `quote_ident` quotes it. A
 * word from a newer release only costs a pair of quotes on an older one.
 */
const NOT_UNRESERVED = new Set([
  "all", "analyse", "analyze", "and", "any", "array", "as", "asc", "asymmetric", "authorization",
  "between", "bigint", "binary", "bit", "boolean", "both", "case", "cast", "char", "character",
  "check", "coalesce", "collate", "collation", "column", "concurrently", "constraint", "create",
  "cross", "current_catalog", "current_date", "current_role", "current_schema", "current_time",
  "current_timestamp", "current_user", "dec", "decimal", "default", "deferrable", "desc",
  "distinct", "do", "else", "end", "except", "exists", "extract", "false", "fetch", "float", "for",
  "foreign", "freeze", "from", "full", "grant", "greatest", "group", "grouping", "having", "ilike",
  "in", "initially", "inner", "inout", "int", "integer", "intersect", "interval", "into", "is",
  "isnull", "join", "json", "json_array", "json_arrayagg", "json_exists", "json_object",
  "json_objectagg", "json_query", "json_scalar", "json_serialize", "json_table", "json_value",
  "lateral", "leading", "least", "left", "like", "limit", "localtime", "localtimestamp",
  "merge_action", "national", "natural", "nchar", "none", "normalize", "not", "notnull", "null",
  "nullif", "numeric", "offset", "on", "only", "or", "order", "out", "outer", "overlaps", "overlay",
  "placing", "position", "precision", "primary", "real", "references", "returning", "right", "row",
  "select", "session_user", "setof", "similar", "smallint", "some", "substring", "symmetric",
  "system_user", "table", "tablesample", "then", "time", "timestamp", "to", "trailing", "treat",
  "trim", "true", "union", "unique", "user", "using", "values", "varchar", "variadic", "verbose",
  "when", "where", "window", "with", "xmlattributes", "xmlconcat", "xmlelement", "xmlexists",
  "xmlforest", "xmlnamespaces", "xmlparse", "xmlpi", "xmlroot", "xmlserialize", "xmltable",
]);

/** What PostgreSQL reads as the same name unquoted: it folds case and allows only these. */
const PLAIN = /^[a-z_][a-z0-9_]*$/;

/** A name as PostgreSQL's `quote_ident` writes it: bare when that reads back as the same name. */
export function quoteIdentifier(name: string): string {
  if (PLAIN.test(name) && !NOT_UNRESERVED.has(name)) return name;
  return `"${name.replace(/"/g, '""')}"`;
}

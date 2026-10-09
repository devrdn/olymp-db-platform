// Package checker parses a participant's query with PostgreSQL's own parser
// and decides whether it stays within a sqlpolicy.Policy. It does not run the
// query; the database grants are a second layer behind it.
package checker

import (
	"errors"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgparser "github.com/pganalyze/pg_query_go/v6/parser"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// maxDepth bounds how deeply nested a query may be, so the recursive walk
// refuses a deeply nested query instead of overflowing the stack.
const maxDepth = 100

// maxQueryBytes bounds the text before the C parser reads it: parsing builds
// a tree several times the input's size, and this package must not rely on
// the HTTP body limit. Shared with the façade so the two bounds agree.
const maxQueryBytes = sqlpolicy.MaxQueryBytes

// Checker decides whether a query stays within a policy. It holds the
// function allow-list, which an operator may extend per installation; the
// policy comes per call, since it belongs to the olympiad.
type Checker struct{ functions map[string]struct{} }

// NewChecker returns a checker whose allow-list is the standard one plus
// whatever the installation has added.
func NewChecker(extra ...string) *Checker {
	functions := make(map[string]struct{}, len(defaultFunctions)+len(extra))
	for name := range defaultFunctions {
		functions[name] = struct{}{}
	}
	for _, name := range extra {
		functions[strings.ToLower(name)] = struct{}{}
	}
	return &Checker{functions: functions}
}

var standard = NewChecker()

// Check reports whether the query is allowed under the policy, using the
// standard function allow-list.
func Check(sql string, p sqlpolicy.Policy) error { return standard.Check(sql, p) }

// Check reports whether the query is allowed under the policy.
func (c *Checker) Check(sql string, p sqlpolicy.Policy) error {
	_, err := c.Analyse(sql, p)
	return err
}

// Analyse checks the query and reports what it is. An invalid policy fails
// closed. The query must be one statement, so the tree checked is all of what
// will run; then the root statement and every node beneath it are checked.
func (c *Checker) Analyse(sql string, p sqlpolicy.Policy) (sqlpolicy.Statement, error) {
	if err := p.Validate(); err != nil {
		return sqlpolicy.Statement{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeInvalidPolicy, Subject: err.Error()}
	}
	if len(sql) > maxQueryBytes {
		return sqlpolicy.Statement{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooLong, Subject: fmt.Sprintf("%d bytes", len(sql))}
	}

	tree, err := pg.Parse(sql)
	if err != nil {
		return sqlpolicy.Statement{}, &sqlpolicy.Refusal{
			Code:    sqlpolicy.CodeParseError,
			Subject: err.Error(),
			// A 1-based offset into sql as sent, as a server would report it.
			Position: parsePosition(err),
		}
	}
	if len(tree.Stmts) != 1 {
		return sqlpolicy.Statement{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotOneStatement, Subject: fmt.Sprintf("%d statements", len(tree.Stmts))}
	}

	raw := tree.Stmts[0]
	root := raw.Stmt
	if root == nil || root.Node == nil {
		return sqlpolicy.Statement{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotOneStatement, Subject: "0 statements"}
	}

	plan, err := c.rootAllowed(root, p)
	if err != nil {
		return sqlpolicy.Statement{}, err
	}
	// A write's own node is not in allowedKinds (that refuses one hidden in a
	// CTE), so at the root only its children are walked.
	if plan.checkSelf {
		err = c.walkNode(plan.node, p, 0)
	} else {
		err = c.walkMessage(plan.node.ProtoReflect(), p, 0)
	}
	if err != nil {
		return sqlpolicy.Statement{}, err
	}
	return sqlpolicy.Statement{
		Text:    statementText(sql, raw),
		Explain: plan.explain,
		Writes:  plan.writes,
		Frees:   plan.frees,
	}, nil
}

// parsePosition reads the character offset a parse error carries, or zero.
func parsePosition(err error) int {
	var perr *pgparser.Error
	if errors.As(err, &perr) {
		return perr.Cursorpos
	}
	return 0
}

// statementText cuts the statement out of sql using the parser's own bounds
// (CLAUDE.md rule 14). A length of zero means "to the end of the input".
// Bounds that do not fit fall back to the whole text rather than a panic.
func statementText(sql string, raw *pg.RawStmt) string {
	start, length := int(raw.GetStmtLocation()), int(raw.GetStmtLen())
	if start < 0 || start > len(sql) {
		return sql
	}
	if length <= 0 || start+length > len(sql) {
		return sql[start:]
	}
	return sql[start : start+length]
}

// rootPlan is what rootAllowed decided about the outermost statement. For
// EXPLAIN, node is the inner query: the options are checked once at the root,
// so DefElem never has to be an allowed node kind.
type rootPlan struct {
	node *pg.Node
	// checkSelf is false for a write, whose own node may appear only at the root.
	checkSelf bool
	explain   bool
	writes    bool
	frees     bool
}

// explainOptions are the EXPLAIN options a participant may pass: the ones
// that only change how the plan is printed.
var explainOptions = map[string]struct{}{
	"verbose": {},
	"costs":   {},
	"format":  {},
	"summary": {},
}

func (c *Checker) rootAllowed(root *pg.Node, p sqlpolicy.Policy) (rootPlan, error) {
	switch stmt := root.Node.(type) {
	case *pg.Node_SelectStmt:
		return rootPlan{node: root, checkSelf: true}, nil
	case *pg.Node_ExplainStmt:
		for _, option := range stmt.ExplainStmt.Options {
			name := strings.ToLower(option.GetDefElem().GetDefname())
			// ANALYZE runs the statement; on a DML it is the DML.
			if name == "analyze" {
				return rootPlan{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: "EXPLAIN ANALYZE"}
			}
			// An allow-list, since PostgreSQL adds options: SETTINGS prints
			// non-default server settings, which the catalog rules hide.
			if _, ok := explainOptions[name]; !ok {
				return rootPlan{}, &sqlpolicy.Refusal{
					Code:    sqlpolicy.CodeStatementNotSupported,
					Subject: "EXPLAIN " + strings.ToUpper(name),
				}
			}
		}
		if stmt.ExplainStmt.Query == nil {
			return rootPlan{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: "EXPLAIN"}
		}
		return rootPlan{node: stmt.ExplainStmt.Query, checkSelf: true, explain: true}, nil
	default:
		if p.Mode != sqlpolicy.ModeReadWrite {
			return rootPlan{}, &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: kindOf(root)}
		}
		if err := c.writeAllowed(root, p); err != nil {
			return rootPlan{}, err
		}
		return rootPlan{node: root, writes: true, frees: freesSpace(root)}, nil
	}
}

// walkNode visits this node and everything beneath it.
func (c *Checker) walkNode(node *pg.Node, p sqlpolicy.Policy, depth int) error {
	if depth > maxDepth {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooDeep, Subject: fmt.Sprintf("more than %d levels", maxDepth)}
	}
	if err := c.visit(node, p); err != nil {
		return err
	}
	return c.walkMessage(node.ProtoReflect(), p, depth)
}

// walkMessage descends through a message's fields, visiting every Node it
// reaches. It uses protobuf reflection rather than a hand-written switch,
// which could forget a field and leave a subtree unchecked.
func (c *Checker) walkMessage(m protoreflect.Message, p sqlpolicy.Policy, depth int) error {
	var failed error

	m.Range(func(fd protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		switch {
		case fd.IsMap():
			// The grammar has none; nothing to descend into.
		case fd.IsList():
			list := value.List()
			for i := range list.Len() {
				if failed = c.enter(list.Get(i).Message(), p, depth+1); failed != nil {
					return false
				}
			}
		default:
			failed = c.enter(value.Message(), p, depth+1)
		}
		return failed == nil
	})

	return failed
}

// enter visits a message, checking it first if it is a grammar node.
func (c *Checker) enter(m protoreflect.Message, p sqlpolicy.Policy, depth int) error {
	if node, ok := m.Interface().(*pg.Node); ok {
		return c.walkNode(node, p, depth)
	}
	// A TypeName on a typed field, not wrapped in a Node (ColumnDef.TypeName
	// in CREATE TABLE), so a reg* type cannot ride a column definition.
	if tn, ok := m.Interface().(*pg.TypeName); ok {
		if err := castAllowed(tn); err != nil {
			return err
		}
	}
	if depth > maxDepth {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooDeep, Subject: fmt.Sprintf("more than %d levels", maxDepth)}
	}
	return c.walkMessage(m, p, depth)
}

// visit checks one node: that its type is one the checker knows, and then
// whatever that particular type carries.
func (c *Checker) visit(node *pg.Node, p sqlpolicy.Policy) error {
	kind := kindOf(node)
	if kind == "" {
		// An empty placeholder in a fixed-shape list (`FROM generate_series(…)`
		// has one for column definitions). It holds no SQL.
		return nil
	}
	if _, known := allowedKinds[kind]; !known {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeConstructNotSupported, Subject: kind}
	}

	switch n := node.Node.(type) {
	case *pg.Node_SelectStmt:
		return selectAllowed(n.SelectStmt)
	case *pg.Node_FuncCall:
		return c.functionAllowed(n.FuncCall)
	case *pg.Node_RangeVar:
		return relationAllowed(n.RangeVar, p)
	case *pg.Node_SqlvalueFunction:
		return sqlValueAllowed(n.SqlvalueFunction)
	case *pg.Node_TypeCast:
		return castAllowed(n.TypeCast.GetTypeName())
	case *pg.Node_TypeName:
		// The Node-wrapped counterpart of the TypeName check in enter.
		return castAllowed(n.TypeName)
	}
	return nil
}

// selectAllowed catches the two SELECTs that are not reads, at every level:
// a subquery may carry an INTO, and a CTE a locking clause.
func selectAllowed(stmt *pg.SelectStmt) error {
	if stmt.IntoClause != nil {
		// `SELECT … INTO notes` is CREATE TABLE AS with a SELECT's node type.
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: "SELECT INTO"}
	}
	if len(stmt.LockingClause) > 0 {
		// FOR UPDATE / FOR SHARE take row locks. A read-only transaction
		// refuses them anyway; this gives a readable reason.
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: "SELECT FOR UPDATE"}
	}
	return nil
}

// sqlValueAllowed admits the time values and refuses the identities that
// share their node type: CURRENT_USER names the role and CURRENT_CATALOG the
// database, whose name encodes the contest and participant. Like the
// sensitive catalogs, they are refused whatever the policy.
func sqlValueAllowed(fn *pg.SQLValueFunction) error {
	switch fn.GetOp() {
	case pg.SQLValueFunctionOp_SVFOP_CURRENT_DATE,
		pg.SQLValueFunctionOp_SVFOP_CURRENT_TIME,
		pg.SQLValueFunctionOp_SVFOP_CURRENT_TIME_N,
		pg.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP,
		pg.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP_N,
		pg.SQLValueFunctionOp_SVFOP_LOCALTIME,
		pg.SQLValueFunctionOp_SVFOP_LOCALTIME_N,
		pg.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP,
		pg.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP_N:
		return nil
	default:
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: sqlValueName(fn.GetOp())}
	}
}

// sqlValueName spells the refused construct as SQL rather than the enum name.
func sqlValueName(op pg.SQLValueFunctionOp) string {
	name := strings.TrimPrefix(op.String(), "SVFOP_")
	return strings.ReplaceAll(strings.TrimSuffix(name, "_N"), "_", " ")
}

// functionAllowed checks a call against the allow-list.
func (c *Checker) functionAllowed(call *pg.FuncCall) error {
	parts := make([]string, 0, len(call.Funcname))
	for _, part := range call.Funcname {
		parts = append(parts, part.GetString_().GetSval())
	}

	name, addressable := functionName(parts)
	if !addressable {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: name}
	}
	if _, allowed := c.functions[name]; !allowed {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: name}
	}
	// Applied whatever the allow-list holds, so an operator's additions keep
	// the constant-size check (generators.go).
	return sizeAllowed(name, call)
}

// relationAllowed checks a table reference against the catalog rules.
func relationAllowed(rel *pg.RangeVar, p sqlpolicy.Policy) error {
	sensitive, catalog := sqlpolicy.ClassifyRelation(rel.GetSchemaname(), rel.GetRelname())

	switch {
	case sensitive:
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeCatalogNotReadable, Subject: rel.GetRelname()}
	case catalog && !p.AllowCatalog:
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeCatalogNotAllowed, Subject: rel.GetRelname()}
	}
	return nil
}

// regTypes are the pseudo-types that resolve a name to an OID through the
// catalog ('name'::regclass, ::regrole, ...). Such a cast is a catalog lookup
// the relation rules never see, reaching every object in the installation,
// so it is refused whatever the policy.
var regTypes = names(
	"regclass", "regproc", "regprocedure", "regoper", "regoperator",
	"regtype", "regrole", "regnamespace", "regconfig", "regdictionary",
	"regcollation",
)

// castAllowed refuses a cast to a reg* pseudo-type, however it is spelled.
func castAllowed(tn *pg.TypeName) error {
	name, isReg := regTypeName(tn)
	if !isReg {
		return nil
	}
	return &sqlpolicy.Refusal{Code: sqlpolicy.CodeCatalogNotReadable, Subject: name}
}

// regTypeName reduces a type name, bare or pg_catalog-qualified (both name
// the same type), to its lower-cased spelling and reports whether it is one
// of regTypes.
func regTypeName(tn *pg.TypeName) (string, bool) {
	parts := make([]string, 0, len(tn.GetNames()))
	for _, part := range tn.GetNames() {
		parts = append(parts, part.GetString_().GetSval())
	}

	var bare string
	switch len(parts) {
	case 1:
		bare = strings.ToLower(parts[0])
	case 2:
		if strings.ToLower(parts[0]) != "pg_catalog" {
			return "", false
		}
		bare = strings.ToLower(parts[1])
	default:
		return "", false
	}
	_, isReg := regTypes[bare]
	return bare, isReg
}

// kindOf names a node by the grammar's own name (`select_stmt`), read off the
// protobuf oneof so it cannot drift from the parser.
func kindOf(node *pg.Node) string {
	m := node.ProtoReflect()
	oneof := m.Descriptor().Oneofs().ByName("node")
	if oneof == nil {
		return ""
	}
	set := m.WhichOneof(oneof)
	if set == nil {
		return ""
	}
	return string(set.Name())
}

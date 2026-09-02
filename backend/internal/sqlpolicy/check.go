package sqlpolicy

import (
	"fmt"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Code names why a query was refused.
//
// Stable, because it is the contract: the HTTP layer maps a code to a sentence
// in each locale, and the journal panel groups refusals by it. The Subject
// alongside carries the offending name — a function, a table, a construct — in
// the parser's own vocabulary, which is what an operator needs in the log.
type Code string

const (
	CodeInvalidPolicy         Code = "invalid_policy"
	CodeModeNotSupported      Code = "mode_not_supported"
	CodeParseError            Code = "parse_error"
	CodeNotOneStatement       Code = "not_one_statement"
	CodeStatementNotSupported Code = "statement_not_supported"
	CodeConstructNotSupported Code = "construct_not_supported"
	CodeFunctionNotSupported  Code = "function_not_supported"
	CodeCatalogNotReadable    Code = "catalog_not_readable"
	CodeCatalogNotAllowed     Code = "catalog_not_allowed"
	CodeTooDeep               Code = "too_deep"
	CodeTooLong               Code = "too_long"
	CodeTableNotWritable      Code = "table_not_writable"
	CodeNotPermitted          Code = "not_permitted"
)

// Refusal is why one query was not allowed to run.
type Refusal struct {
	Code    Code
	Subject string
}

func (r *Refusal) Error() string {
	if r.Subject == "" {
		return string(r.Code)
	}
	return fmt.Sprintf("%s: %s", r.Code, r.Subject)
}

// maxDepth bounds how deeply nested a query may be.
//
// Not a policy rule but a guard on this package itself: the walk is recursive,
// and a query nested thousands of levels deep is a cheap way to end the
// process with a stack overflow rather than a refusal. Far above anything a
// person writes; the parser gives up before this on most shapes anyway.
const maxDepth = 100

// maxQueryBytes bounds the text before it is parsed.
//
// Also a guard on this package rather than a rule about SQL. Parsing builds a
// tree several times the size of its input, and the HTTP layer's own body
// limit is a different layer's decision that this one should not depend on
// being set. Far longer than any query a person writes by hand.
const maxQueryBytes = 64 << 10

// Checker decides whether a query stays within a policy.
//
// It exists as a type rather than a function because the function allow-list
// is installation configuration: an operator extends it after a pilot without
// waiting for a release (section 5, point 3). The policy comes per call, since
// it belongs to the olympiad.
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
func Check(sql string, p Policy) error { return standard.Check(sql, p) }

// Statement is what the checker learned about a query it allowed.
//
// Small on purpose: it carries only what a caller cannot work out again
// without parsing the query a second time, and the caller that needs it — the
// Query Runner — would otherwise be reduced to matching a prefix, which a
// leading comment defeats.
type Statement struct {
	// Explain reports an EXPLAIN. It matters because an EXPLAIN cannot be
	// placed inside a subquery, so it is the one shape that must not be
	// wrapped when a result is limited.
	Explain bool
	// Writes reports a statement that changes the database. The disk quota is
	// checked before one of these and not before a read, because a read cannot
	// fill a disk and the check costs a round trip.
	Writes bool
}

// Check reports whether the query is allowed under the policy.
func (c *Checker) Check(sql string, p Policy) error {
	_, err := c.Analyse(sql, p)
	return err
}

// Analyse checks the query and reports what it is.
//
// The order is deliberate. The policy is validated first, because an incoherent
// policy cannot decide anything and failing closed is the only safe answer.
// Then the text must parse, and be exactly one statement — everything after
// that reasons about a tree, and reasoning about a tree that represents only
// the first half of what will run is how a checker gets walked past. Only then
// the shape: the root statement, and then every node beneath it.
func (c *Checker) Analyse(sql string, p Policy) (Statement, error) {
	if err := p.Validate(); err != nil {
		return Statement{}, &Refusal{Code: CodeInvalidPolicy, Subject: err.Error()}
	}
	if len(sql) > maxQueryBytes {
		return Statement{}, &Refusal{Code: CodeTooLong, Subject: fmt.Sprintf("%d bytes", len(sql))}
	}

	tree, err := pg.Parse(sql)
	if err != nil {
		return Statement{}, &Refusal{Code: CodeParseError, Subject: err.Error()}
	}
	if len(tree.Stmts) != 1 {
		return Statement{}, &Refusal{Code: CodeNotOneStatement, Subject: fmt.Sprintf("%d statements", len(tree.Stmts))}
	}

	root := tree.Stmts[0].Stmt
	if root == nil || root.Node == nil {
		return Statement{}, &Refusal{Code: CodeNotOneStatement, Subject: "0 statements"}
	}

	plan, err := c.rootAllowed(root, p)
	if err != nil {
		return Statement{}, err
	}
	// A write statement's own node is not in the allowed set — that is what
	// refuses one hidden inside a CTE — so its children are walked without
	// checking the node itself, which the root check has already decided.
	if plan.checkSelf {
		err = c.walkNode(plan.node, p, 0)
	} else {
		err = c.walkMessage(plan.node.ProtoReflect(), p, 0)
	}
	if err != nil {
		return Statement{}, err
	}
	return Statement{Explain: plan.explain, Writes: plan.writes}, nil
}

// rootAllowed checks the outermost statement and returns the node to walk.
//
// EXPLAIN returns its inner query rather than itself: its options are checked
// here, once, so that DefElem never has to be a generally-allowed node type.
type rootPlan struct {
	node *pg.Node
	// checkSelf is false for a write, whose own node is deliberately absent
	// from the allowed set so that it cannot appear anywhere but the root.
	checkSelf bool
	explain   bool
	writes    bool
}

func (c *Checker) rootAllowed(root *pg.Node, p Policy) (rootPlan, error) {
	switch stmt := root.Node.(type) {
	case *pg.Node_SelectStmt:
		return rootPlan{node: root, checkSelf: true}, nil
	case *pg.Node_ExplainStmt:
		for _, option := range stmt.ExplainStmt.Options {
			name := strings.ToLower(option.GetDefElem().GetDefname())
			// ANALYZE is not a plan, it is a run — on a DML it *is* the DML.
			// The rest (VERBOSE, COSTS, FORMAT) only change the printout.
			if name == "analyze" {
				return rootPlan{}, &Refusal{Code: CodeStatementNotSupported, Subject: "EXPLAIN ANALYZE"}
			}
		}
		if stmt.ExplainStmt.Query == nil {
			return rootPlan{}, &Refusal{Code: CodeStatementNotSupported, Subject: "EXPLAIN"}
		}
		return rootPlan{node: stmt.ExplainStmt.Query, checkSelf: true, explain: true}, nil
	default:
		if p.Mode != ModeReadWrite {
			return rootPlan{}, &Refusal{Code: CodeStatementNotSupported, Subject: kindOf(root)}
		}
		if err := c.writeAllowed(root, p); err != nil {
			return rootPlan{}, err
		}
		return rootPlan{node: root, writes: true}, nil
	}
}

// walkNode visits this node and everything beneath it.
func (c *Checker) walkNode(node *pg.Node, p Policy, depth int) error {
	if depth > maxDepth {
		return &Refusal{Code: CodeTooDeep, Subject: fmt.Sprintf("more than %d levels", maxDepth)}
	}
	if err := c.visit(node, p); err != nil {
		return err
	}
	return c.walkMessage(node.ProtoReflect(), p, depth)
}

// walkMessage descends through a message's fields, visiting every Node it
// reaches.
//
// Driven by protobuf reflection rather than by a hand-written switch over the
// grammar. A switch would list the fields somebody thought of, and the first
// grammar node whose child field was forgotten would be a subtree nobody
// checks — silently, and only for the queries that reach it. Reflection cannot
// forget a field, so an unknown construct is refused by the visit below rather
// than skipped by the walk.
func (c *Checker) walkMessage(m protoreflect.Message, p Policy, depth int) error {
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
func (c *Checker) enter(m protoreflect.Message, p Policy, depth int) error {
	if node, ok := m.Interface().(*pg.Node); ok {
		return c.walkNode(node, p, depth)
	}
	if depth > maxDepth {
		return &Refusal{Code: CodeTooDeep, Subject: fmt.Sprintf("more than %d levels", maxDepth)}
	}
	return c.walkMessage(m, p, depth)
}

// visit checks one node: that its type is one the checker knows, and then
// whatever that particular type carries.
func (c *Checker) visit(node *pg.Node, p Policy) error {
	kind := kindOf(node)
	if kind == "" {
		// A node with nothing set is a placeholder the grammar leaves in a
		// fixed-shape list — `FROM generate_series(…)` carries one where the
		// column definitions would go. It holds no SQL, so there is nothing to
		// allow or refuse; anything actually present would have a kind. Left
		// unhandled it refused every set-returning function in a FROM clause,
		// with an empty name in the message.
		return nil
	}
	if _, known := allowedKinds[kind]; !known {
		return &Refusal{Code: CodeConstructNotSupported, Subject: kind}
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
	}
	return nil
}

// selectAllowed catches the two SELECTs that are not reads.
//
// Both matter at every level, not only at the root: a subquery may carry an
// INTO, and a CTE may carry a locking clause.
func selectAllowed(stmt *pg.SelectStmt) error {
	if stmt.IntoClause != nil {
		// `SELECT … INTO notes FROM …` is CREATE TABLE AS wearing a SELECT's
		// node type. A check that trusted the root would let it through.
		return &Refusal{Code: CodeStatementNotSupported, Subject: "SELECT INTO"}
	}
	if len(stmt.LockingClause) > 0 {
		// FOR UPDATE / FOR SHARE take row locks. A read-only transaction
		// refuses them anyway; saying so here turns a database error nobody
		// can read into a sentence about the query.
		return &Refusal{Code: CodeStatementNotSupported, Subject: "SELECT FOR UPDATE"}
	}
	return nil
}

// sqlValueAllowed splits one node type that carries two different things.
//
// `CURRENT_DATE` and `CURRENT_USER` are the same node with a different `op`,
// which is the one place where a check by node type alone is too coarse. The
// times are the game's; the identities are the installation's — `CURRENT_USER`
// names the database role, and `CURRENT_CATALOG` names the database, whose
// name encodes the contest and the participant. Those belong to the same class
// as the sensitive catalogs, which are never readable however the policy is
// set, so they are refused the same way.
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
		return &Refusal{Code: CodeFunctionNotSupported, Subject: sqlValueName(fn.GetOp())}
	}
}

// sqlValueName spells the refused construct the way it was written, rather
// than as the enum constant, so the journal reads like SQL.
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
		return &Refusal{Code: CodeFunctionNotSupported, Subject: name}
	}
	if _, allowed := c.functions[name]; !allowed {
		return &Refusal{Code: CodeFunctionNotSupported, Subject: name}
	}
	return nil
}

// relationAllowed checks a table reference against the catalog rules.
func relationAllowed(rel *pg.RangeVar, p Policy) error {
	sensitive, catalog := classifyRelation(rel.GetSchemaname(), rel.GetRelname())

	switch {
	case sensitive:
		// Never, whatever the policy says: these describe the installation and
		// the other participants, not the game.
		return &Refusal{Code: CodeCatalogNotReadable, Subject: rel.GetRelname()}
	case catalog && !p.AllowCatalog:
		return &Refusal{Code: CodeCatalogNotAllowed, Subject: rel.GetRelname()}
	}
	return nil
}

// kindOf names a node by the grammar's own name for it — `select_stmt`,
// `variable_set_stmt` — read off the protobuf oneof rather than from a table
// this package would have to keep in step with the parser.
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

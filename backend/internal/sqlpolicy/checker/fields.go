package checker

import (
	"slices"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// PostgreSQL reads `x.f` as the call `f(x)` whenever f is not a column of x,
// and the parse tree then holds no function call for the allow-list to read.
// Without a schema the checker cannot tell a column from a function, so the
// two spellings that can reach a function with a scalar argument are refused
// by shape: a field selected from an expression, and a field of a function in
// FROM that its alias does not declare. A table's or a subquery's `t.f` passes
// a whole row, which no function behind the database's own REVOKEs accepts.

// indirectionAllowed refuses `(expression).field`. Subscripts and `.*` name
// no function and stay.
func indirectionAllowed(ind *pg.A_Indirection) error {
	for _, step := range ind.GetIndirection() {
		if field := step.GetString_(); field != nil {
			return fieldSelectionRefused("", field.GetSval())
		}
	}
	return nil
}

// functionFieldsAllowed refuses `g.f` where g is a function in FROM, anywhere
// in the statement, unless f is a column g's alias declares. It must run after
// the walk, which is what bounds the depth this recursion reaches.
//
// Names are matched across the whole statement rather than by scope, so a
// table in a subquery that reuses a function's alias is held to the same rule.
func functionFieldsAllowed(root *pg.Node) error {
	var found functionReferences
	found.collect(root.ProtoReflect())
	if found.unnamed {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeConstructNotSupported, Subject: "an expression in FROM without an alias"}
	}

	for _, ref := range found.qualified {
		qualifier, field := ref[0], ref[1]
		if columns, isFunction := found.declared[qualifier]; isFunction && !slices.Contains(columns, field) {
			return fieldSelectionRefused(qualifier, field)
		}
	}
	return nil
}

func fieldSelectionRefused(qualifier, field string) error {
	return &sqlpolicy.Refusal{
		Code:    sqlpolicy.CodeConstructNotSupported,
		Subject: "field selection " + qualifier + "." + field,
	}
}

// functionReferences is what one statement says about its functions in FROM.
type functionReferences struct {
	// declared maps the name each function answers to onto the columns its
	// alias declares.
	declared map[string][]string
	// qualified holds every two-part column reference, as qualifier and field.
	qualified [][2]string
	// unnamed reports a function in FROM whose name could not be read.
	unnamed bool
}

func (f *functionReferences) collect(m protoreflect.Message) {
	if node, ok := m.Interface().(*pg.Node); ok {
		switch n := node.Node.(type) {
		case *pg.Node_RangeFunction:
			f.declare(n.RangeFunction)
		case *pg.Node_ColumnRef:
			if fields := n.ColumnRef.GetFields(); len(fields) == 2 {
				qualifier, field := fields[0].GetString_(), fields[1].GetString_()
				if qualifier != nil && field != nil {
					f.qualified = append(f.qualified, [2]string{qualifier.GetSval(), field.GetSval()})
				}
			}
		}
	}

	m.Range(func(fd protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case fd.Kind() != protoreflect.MessageKind || fd.IsMap():
		case fd.IsList():
			list := value.List()
			for i := range list.Len() {
				f.collect(list.Get(i).Message())
			}
		default:
			f.collect(value.Message())
		}
		return true
	})
}

func (f *functionReferences) declare(fn *pg.RangeFunction) {
	name, named := functionAlias(fn)
	if !named {
		f.unnamed = true
		return
	}

	var columns []string
	for _, column := range fn.GetAlias().GetColnames() {
		columns = append(columns, column.GetString_().GetSval())
	}
	for _, definition := range fn.GetColdeflist() {
		columns = append(columns, definition.GetColumnDef().GetColname())
	}
	if f.declared == nil {
		f.declared = make(map[string][]string)
	}
	f.declared[name] = append(f.declared[name], columns...)
}

// functionAlias is the name a function in FROM answers to: its alias, or the
// function's own name. Only a plain call's name can be read here; PostgreSQL
// also names COALESCE(…), CAST(…) and the like when they stand in FROM.
func functionAlias(fn *pg.RangeFunction) (string, bool) {
	if alias := fn.GetAlias(); alias != nil {
		return alias.GetAliasname(), true
	}
	if len(fn.GetFunctions()) == 0 {
		return "", false
	}
	parts := fn.GetFunctions()[0].GetList().GetItems()
	if len(parts) == 0 {
		return "", false
	}
	name := parts[0].GetFuncCall().GetFuncname()
	if len(name) == 0 {
		return "", false
	}
	return name[len(name)-1].GetString_().GetSval(), true
}

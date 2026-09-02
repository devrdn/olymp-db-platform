package checker

import (
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	pg "github.com/pganalyze/pg_query_go/v6"
)

// The statements a contest may permit beyond reading, and what each one has to
// satisfy.
//
// All of them are checked at the root and nowhere else. That is stricter than
// SQL allows — a data-modifying CTE is legal and stays refused — and it is
// what section 5 describes: a list of permitted *root* statements. The reason
// is that the walk beneath cannot tell a write meant by the participant from
// one hidden four levels down in a query that reads like a SELECT, and the
// difference matters more than the convenience.
//
// The other rule is that a participant's own objects live in `work` and
// nowhere else. It is checked here as well as granted in the template, because
// a refusal here says which schema, and a missing privilege says only
// "permission denied".

// writeAllowed decides one write statement.
func (c *Checker) writeAllowed(root *pg.Node, p sqlpolicy.Policy) error {
	switch stmt := root.Node.(type) {
	case *pg.Node_InsertStmt:
		return targetAllowed(p, stmt.InsertStmt.GetRelation(), "INSERT")
	case *pg.Node_UpdateStmt:
		return targetAllowed(p, stmt.UpdateStmt.GetRelation(), "UPDATE")
	case *pg.Node_DeleteStmt:
		return targetAllowed(p, stmt.DeleteStmt.GetRelation(), "DELETE")

	case *pg.Node_ViewStmt:
		if !p.AllowCreateView {
			return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "CREATE VIEW"}
		}
		return ownObject(stmt.ViewStmt.GetView(), "CREATE VIEW")

	case *pg.Node_CreateStmt:
		return createdTableAllowed(p, stmt.CreateStmt.GetRelation())

	case *pg.Node_CreateTableAsStmt:
		return createdTableAllowed(p, stmt.CreateTableAsStmt.GetInto().GetRel())

	case *pg.Node_DropStmt:
		return dropAllowed(p, stmt.DropStmt)
	}
	return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: kindOf(root)}
}

// targetAllowed checks what a write is aimed at.
//
// Two ways to qualify: a game table the contest named, or something in the
// participant's own schema. Anything else is refused by name, because "which
// table" is the one thing the participant needs to be told.
func targetAllowed(p sqlpolicy.Policy, rel *pg.RangeVar, what string) error {
	if rel == nil {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: what}
	}
	if p.MayWriteTo(rel.GetSchemaname(), rel.GetRelname()) {
		return nil
	}
	if p.AllowOwnTables && rel.GetSchemaname() == sqlpolicy.WorkSchema {
		return nil
	}
	return &sqlpolicy.Refusal{Code: sqlpolicy.CodeTableNotWritable, Subject: relationName(rel)}
}

// createdTableAllowed covers CREATE TABLE and CREATE TABLE AS.
//
// A temporary table is a separate permission from a permanent one: it lives
// for the query and cannot be a way to keep something the contest did not mean
// to allow.
func createdTableAllowed(p sqlpolicy.Policy, rel *pg.RangeVar) error {
	if rel == nil {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: "CREATE TABLE"}
	}

	// `t` is the grammar's mark for a temporary relation.
	if rel.GetRelpersistence() == "t" {
		if !p.AllowTempTables {
			return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "CREATE TEMPORARY TABLE"}
		}
		return nil
	}

	if !p.AllowOwnTables {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "CREATE TABLE"}
	}
	return ownObject(rel, "CREATE TABLE")
}

// dropAllowed covers removing what a participant made, and only that.
func dropAllowed(p sqlpolicy.Policy, stmt *pg.DropStmt) error {
	permitted := map[pg.ObjectType]bool{
		pg.ObjectType_OBJECT_TABLE: p.AllowOwnTables,
		pg.ObjectType_OBJECT_VIEW:  p.AllowCreateView,
	}

	allowed, known := permitted[stmt.GetRemoveType()]
	if !known {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: stmt.GetRemoveType().String()}
	}
	if !allowed {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "DROP"}
	}

	for _, object := range stmt.GetObjects() {
		// A dropped object is spelled as a list of name parts. Unqualified, it
		// resolves through the search path, which is not something a refusal
		// should have to guess at — so it is refused for the same reason an
		// unqualified CREATE is.
		parts := object.GetList().GetItems()
		if len(parts) != 2 || parts[0].GetString_().GetSval() != sqlpolicy.WorkSchema {
			return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "DROP outside " + sqlpolicy.WorkSchema}
		}
	}
	return nil
}

// ownObject insists that what a participant creates goes in their own schema.
//
// Unqualified is refused rather than assumed: it would resolve through the
// search path, and a participant who meant `work.notes` and typed `notes`
// should be told so rather than find out from a privilege error.
func ownObject(rel *pg.RangeVar, what string) error {
	if rel.GetSchemaname() != sqlpolicy.WorkSchema {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: what + " outside " + sqlpolicy.WorkSchema}
	}
	return nil
}

// relationName spells a table the way the participant wrote it.
func relationName(rel *pg.RangeVar) string {
	if schema := rel.GetSchemaname(); schema != "" {
		return schema + "." + rel.GetRelname()
	}
	return rel.GetRelname()
}

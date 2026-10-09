package checker

import (
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	pg "github.com/pganalyze/pg_query_go/v6"
)

// Write statements are permitted only at the root, so a write hidden in a
// query that reads like a SELECT stays refused. A participant's own objects
// live in `work` only; that is checked here as well as granted, so the
// refusal names the schema.

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

	case *pg.Node_TruncateStmt:
		return truncateAllowed(p, stmt.TruncateStmt)
	}
	return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: kindOf(root)}
}

// freesSpace reports a statement that can only make the database smaller,
// which the Query Runner admits at the disk quota. TRUNCATE and DROP unlink
// files; DELETE leaves its pages allocated, so it is not one. It decides
// nothing about authority: writeAllowed already did.
func freesSpace(root *pg.Node) bool {
	switch root.Node.(type) {
	case *pg.Node_TruncateStmt, *pg.Node_DropStmt:
		return true
	}
	return false
}

// truncateAllowed admits TRUNCATE on tables a DELETE could target, so it adds
// no authority, only returns pages. CASCADE is refused: it reaches tables
// holding a foreign key, which neither the policy nor the query named.
func truncateAllowed(p sqlpolicy.Policy, stmt *pg.TruncateStmt) error {
	if stmt.GetBehavior() == pg.DropBehavior_DROP_CASCADE {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "TRUNCATE CASCADE"}
	}
	relations := stmt.GetRelations()
	if len(relations) == 0 {
		return &sqlpolicy.Refusal{Code: sqlpolicy.CodeStatementNotSupported, Subject: "TRUNCATE"}
	}
	for _, relation := range relations {
		if err := targetAllowed(p, relation.GetRangeVar(), "TRUNCATE"); err != nil {
			return err
		}
	}
	return nil
}

// targetAllowed admits a game table the contest named, or a table in the
// participant's own schema; anything else is refused by name.
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

// createdTableAllowed covers CREATE TABLE and CREATE TABLE AS. A temporary
// table, which lives for the query, is a separate permission.
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
		// Must be qualified with `work`; unqualified would resolve through
		// the search path.
		parts := object.GetList().GetItems()
		if len(parts) != 2 || parts[0].GetString_().GetSval() != sqlpolicy.WorkSchema {
			return &sqlpolicy.Refusal{Code: sqlpolicy.CodeNotPermitted, Subject: "DROP outside " + sqlpolicy.WorkSchema}
		}
	}
	return nil
}

// ownObject insists that what a participant creates is qualified with their
// own schema. Unqualified is refused rather than resolved via the search path.
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

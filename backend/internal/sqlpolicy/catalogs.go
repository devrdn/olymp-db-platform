package sqlpolicy

import (
	"slices"
	"strings"
)

// The system catalogs split in two, and the split is a policy decision rather
// than a technical one.
//
// Structural catalogs describe the shape of the data: which tables exist and
// what columns they have. Reading them is part of the exercise — a detective
// looks at the filing cabinet before opening a drawer — so they are readable
// by default and Policy.AllowCatalog turns them off for a contest that wants
// the schema discovered some other way.
//
// Sensitive catalogs describe the *installation*: other databases, other
// people's sessions, roles, settings. None of that is the participant's game,
// and pg_stat_activity in particular shows the queries other participants are
// running, which in an olympiad is simply the answers. These are refused
// whatever the policy says, and REVOKE'd in the template as well: two layers,
// because the checker is the one that can have a bug.
var (
	sensitiveCatalogs = map[string]struct{}{
		"pg_database":         {},
		"pg_stat_activity":    {},
		"pg_roles":            {},
		"pg_user":             {},
		"pg_shadow":           {},
		"pg_authid":           {},
		"pg_settings":         {},
		"pg_stat_statements":  {},
		"pg_file_settings":    {},
		"pg_hba_file_rules":   {},
		"pg_stat_replication": {},
	}

	// Schemas whose contents are catalog rather than game data.
	catalogSchemas = map[string]struct{}{
		"pg_catalog":         {},
		"information_schema": {},
		"pg_toast":           {},
	}
)

// classifyRelation decides what a table reference in the query is.
//
// Both the schema and the bare name are considered: `pg_catalog.pg_database`
// and an unqualified `pg_database` are the same table, because pg_catalog is
// on the search path implicitly. Refusing only the qualified spelling would
// be a check anyone gets past by deleting eleven characters.
func classifyRelation(schema, name string) (sensitive, catalog bool) {
	schema, name = strings.ToLower(schema), strings.ToLower(name)

	if _, yes := sensitiveCatalogs[name]; yes {
		return true, true
	}
	if _, yes := catalogSchemas[schema]; yes {
		return false, true
	}
	// The pg_ prefix covers the rest of the catalog without listing it: any
	// pg_* relation is PostgreSQL's, not the game's. Unknown ones land on the
	// stricter side, which is the direction an allow-list should fail in.
	if strings.HasPrefix(name, "pg_") {
		return false, true
	}
	return false, false
}

// SensitiveCatalogs names the relations that must never be readable, sorted.
//
// Exported because the same list has to reach the database as REVOKEs
// (internal/gamedb): the architecture's rule is that the validator and the
// privileges are built from one description so they cannot drift, and two
// hand-kept lists drift the moment one of them is edited. A test in gamedb
// asserts that every name here is actually revoked.
func SensitiveCatalogs() []string {
	out := make([]string, 0, len(sensitiveCatalogs))
	for name := range sensitiveCatalogs {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

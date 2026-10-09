package sqlpolicy

import (
	"slices"
	"strings"
)

// Structural catalogs (tables, columns) are part of the exercise and readable
// unless Policy.AllowCatalog is off. Sensitive catalogs describe the
// installation: other databases, sessions, roles, settings; pg_stat_activity
// shows other participants' queries, which are the answers. Those are refused
// whatever the policy says and also REVOKE'd in the template, in case the
// checker has a bug.
var (
	sensitiveCatalogs = map[string]struct{}{
		// Who exists.
		"pg_database":        {},
		"pg_roles":           {},
		"pg_user":            {},
		"pg_shadow":          {},
		"pg_authid":          {},
		"pg_auth_members":    {},
		"pg_db_role_setting": {},
		// What everybody else is doing. Each of these names other databases
		// or sessions, so refusing pg_database alone would not hide them.
		"pg_stat_activity":              {},
		"pg_stat_database":              {},
		"pg_stat_database_conflicts":    {},
		"pg_locks":                      {},
		"pg_prepared_xacts":             {},
		"pg_stat_ssl":                   {},
		"pg_stat_gssapi":                {},
		"pg_stat_progress_vacuum":       {},
		"pg_stat_progress_analyze":      {},
		"pg_stat_progress_cluster":      {},
		"pg_stat_progress_create_index": {},
		"pg_stat_progress_basebackup":   {},
		"pg_stat_progress_copy":         {},
		// How the installation is set up.
		"pg_settings":          {},
		"pg_stat_statements":   {},
		"pg_file_settings":     {},
		"pg_hba_file_rules":    {},
		"pg_stat_replication":  {},
		"pg_replication_slots": {},
		"pg_stat_wal_receiver": {},
		"pg_stat_subscription": {},
		"pg_shdescription":     {},
		"pg_shseclabel":        {},
	}

	// Schemas whose contents are catalog rather than game data.
	catalogSchemas = map[string]struct{}{
		"pg_catalog":         {},
		"information_schema": {},
		"pg_toast":           {},
	}
)

// ClassifyRelation decides what a table reference in the query is. The bare
// name is checked as well as the schema, because pg_catalog is implicitly on
// the search path and an unqualified `pg_database` is the same table.
func ClassifyRelation(schema, name string) (sensitive, catalog bool) {
	schema, name = strings.ToLower(schema), strings.ToLower(name)

	if _, yes := sensitiveCatalogs[name]; yes {
		return true, true
	}
	if _, yes := catalogSchemas[schema]; yes {
		return false, true
	}
	// Any other pg_* relation is PostgreSQL's, not the game's: unknown names
	// fail toward the stricter side.
	if strings.HasPrefix(name, "pg_") {
		return false, true
	}
	return false, false
}

// SensitiveCatalogs names the relations that must never be readable, sorted.
// internal/gamedb REVOKEs the same list, so the checker and the privileges
// come from one description; a gamedb test asserts each is revoked.
func SensitiveCatalogs() []string {
	out := make([]string, 0, len(sensitiveCatalogs))
	for name := range sensitiveCatalogs {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

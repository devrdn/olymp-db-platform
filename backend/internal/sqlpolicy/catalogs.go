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
		// Cluster-wide: the shared catalogs and the server-wide statistics,
		// which describe the installation rather than the game. Some exist
		// only on newer servers; the REVOKE skips a name the server lacks.
		"pg_group":                     {},
		"pg_shdepend":                  {},
		"pg_tablespace":                {},
		"pg_subscription":              {},
		"pg_replication_origin":        {},
		"pg_replication_origin_status": {},
		"pg_parameter_acl":             {},
		"pg_user_mappings":             {},
		"pg_stat_bgwriter":             {},
		"pg_stat_checkpointer":         {},
		"pg_stat_io":                   {},
		"pg_stat_wal":                  {},
		"pg_stat_slru":                 {},
		"pg_stat_archiver":             {},
		"pg_stat_recovery_prefetch":    {},
		"pg_stat_subscription_stats":   {},
	}

	// Schemas whose contents are catalog rather than game data.
	catalogSchemas = map[string]struct{}{
		"pg_catalog":         {},
		"information_schema": {},
		"pg_toast":           {},
	}
)

// sensitiveFunctions are the built-in functions PostgreSQL lets every role
// execute that reach past a participant's own data or hold the cluster up.
// The checker's allow-list already leaves them out; the template REVOKEs them
// as well, because PostgreSQL reads `x.f` as `f(x)` when f is no column, and
// such a call has no function node for the allow-list to see. A whole family
// is listed even where one member is harmless without another.
var sensitiveFunctions = []string{
	// What the other sessions are doing: the functions the sensitive session
	// catalogs are views over, and the lock-wait questions that take a pid.
	// Every participant connects as the same role, and PostgreSQL shows a role
	// the queries of its own sessions.
	"pg_stat_get_activity",
	"pg_stat_get_backend_activity", "pg_stat_get_backend_activity_start",
	"pg_stat_get_backend_client_addr", "pg_stat_get_backend_client_port",
	"pg_stat_get_backend_dbid", "pg_stat_get_backend_idset",
	"pg_stat_get_backend_pid", "pg_stat_get_backend_start",
	"pg_stat_get_backend_subxact", "pg_stat_get_backend_userid",
	"pg_stat_get_backend_wait_event", "pg_stat_get_backend_wait_event_type",
	"pg_stat_get_backend_xact_start",
	"pg_stat_get_progress_info", "pg_lock_status", "pg_prepared_xact",
	"pg_blocking_pids", "pg_safe_snapshot_blocking_pids",
	"pg_isolation_test_session_is_blocked",

	// Interrupting them: a role may signal its own sessions, and the role is
	// shared.
	"pg_cancel_backend", "pg_terminate_backend",

	// Running SQL handed over as text or as a relation's name, which no
	// checker rule reads.
	"ts_stat", "ts_rewrite",
	"query_to_xml", "query_to_xmlschema", "query_to_xml_and_xmlschema",
	"cursor_to_xml", "cursor_to_xmlschema",
	"table_to_xml", "table_to_xmlschema", "table_to_xml_and_xmlschema",
	"schema_to_xml", "schema_to_xmlschema", "schema_to_xml_and_xmlschema",
	"database_to_xml", "database_to_xmlschema", "database_to_xml_and_xmlschema",

	// Reading and changing settings: what pg_settings is a view over.
	"current_setting", "set_config", "pg_show_all_settings",

	// Holding one of the few running slots for the whole timeout.
	"pg_sleep", "pg_sleep_for", "pg_sleep_until",

	// Advisory locks: each takes an entry in the lock table every database
	// shares, and a full table refuses locks to everybody.
	"pg_advisory_lock", "pg_advisory_lock_shared",
	"pg_advisory_xact_lock", "pg_advisory_xact_lock_shared",
	"pg_try_advisory_lock", "pg_try_advisory_lock_shared",
	"pg_try_advisory_xact_lock", "pg_try_advisory_xact_lock_shared",
	"pg_advisory_unlock", "pg_advisory_unlock_shared", "pg_advisory_unlock_all",

	// Large objects: storage any role may create, which no table grant covers.
	"lo_creat", "lo_create", "lo_from_bytea", "lo_open", "lo_close",
	"lo_get", "lo_put", "lo_unlink", "loread", "lowrite",
	"lo_lseek", "lo_lseek64", "lo_tell", "lo_tell64",
	"lo_truncate", "lo_truncate64",

	// The notification queue is one for the whole cluster.
	"pg_notify",
}

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

// SensitiveFunctions names the pg_catalog functions no participant may
// execute, sorted. internal/gamedb REVOKEs every overload of each; a checker
// test asserts none of them is on the function allow-list.
func SensitiveFunctions() []string {
	out := slices.Clone(sensitiveFunctions)
	slices.Sort(out)
	return out
}

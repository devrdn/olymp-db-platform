// Package migrations embeds the core database schema migrations.
//
// Embedding them in the binary means the deployed image always carries the
// exact migrations that match its code, with no separate file mount or CLI
// tool to keep in sync.
package migrations

import "embed"

// FS holds every migration file, named {version}_{title}.{up|down}.sql.
//
//go:embed *.sql
var FS embed.FS

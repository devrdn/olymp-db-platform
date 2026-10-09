// Package migrations embeds the core database schema migrations, so a
// deployed image always carries the migrations that match its code. It does
// not apply them; cmd/migrate does.
package migrations

import "embed"

// FS holds every migration file, named {version}_{title}.{up|down}.sql.
//
//go:embed *.sql
var FS embed.FS

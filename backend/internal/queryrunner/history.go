package queryrunner

import (
	"time"
)

// HistoryEntry is one row of a participant's own query log (query_log), read
// back.
type HistoryEntry struct {
	SQL string
	// SQLTruncated says SQL was cut at MaxHistorySQLChars, so a shortened
	// statement is never shown as whole. Never set by the streamed export.
	SQLTruncated bool
	Status       Status
	// Error is empty for a query that did not fail.
	Error string
	// DurationMs and RowCount are nil, not zero, for a row still `running`:
	// in flight, or abandoned and not yet swept.
	DurationMs *int
	RowCount   *int
	ExecutedAt time.Time
}

// DefaultHistoryLimit and MaxHistoryLimit bound one page of a participant's
// own query log (CLAUDE.md rule 2).
const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 200
)

// MaxHistorySQLChars bounds the statement one row of a page carries, in
// characters, so a full page stays under 800 KiB instead of 200 statements of
// 64 KiB (CLAUDE.md rule 2). It is applied in the SELECT, where the bytes
// arrive (rule 12). The CSV export still carries every statement whole.
const MaxHistorySQLChars = 1000

// MaxExportRows and MaxExportBytes bound one CSV download of a participant's
// own log (CLAUDE.md rule 2). The export holds a core pool connection while it
// streams, so it must end. Both are safety nets no real contest reaches: 20,000
// rows is eleven hours at the default rate, and the byte bound covers 64 KiB
// statements. When one binds, the file keeps the oldest rows and says so in a
// final line.
const (
	MaxExportRows  = 20_000
	MaxExportBytes = 32 << 20
)

// NormalizeHistoryPage clamps a requested page to the history bounds, like
// audit.Filter.Normalize.
func NormalizeHistoryPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	if limit > MaxHistoryLimit {
		limit = MaxHistoryLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

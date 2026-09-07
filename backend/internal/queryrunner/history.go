package queryrunner

import (
	"time"
)

// HistoryEntry is one row of a participant's own query log, read back rather
// than written — the same table Journal writes to (query_log), the other
// direction. It carries exactly what the log already records for this
// purpose: the statement, how it ended, and when.
type HistoryEntry struct {
	SQL    string
	Status Status
	// Error is empty for a query that did not fail.
	Error string
	// DurationMs and RowCount are nil for a row still `running` — a query in
	// flight the instant this is read, or one whose process died before
	// SweepAbandoned closed it. Nil rather than zero standing in for "not
	// yet recorded", the same distinction ParticipantQuestion's own
	// AttemptsRemaining keeps on the read side of a different table.
	DurationMs *int
	RowCount   *int
	ExecutedAt time.Time
}

// DefaultHistoryLimit and MaxHistoryLimit bound one page of a participant's
// own query log.
//
// CLAUDE.md rule 2: the bound belongs in the domain, not only in the 1 MiB
// request-body limit that would otherwise be the only ceiling on how large a
// page a client could ask for — and a query log can run to hundreds of rows
// over a two-hour contest, unlike most lists this size a participant reads.
const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 200
)

// NormalizeHistoryPage clamps a requested page to what History implementations
// actually honour, mirroring audit.Filter.Normalize — the existing pattern for
// a paged, append-mostly journal a caller pages through newest-first.
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

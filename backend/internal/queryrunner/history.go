package queryrunner

import (
	"time"
)

// HistoryEntry is one row of a participant's own query log, read back rather
// than written — the same table Journal writes to (query_log), the other
// direction. It carries exactly what the log already records for this
// purpose: the statement, how it ended, and when.
type HistoryEntry struct {
	SQL string
	// SQLTruncated says SQL is the beginning of the statement rather than the
	// whole of it — see MaxHistorySQLChars. A flag rather than a silent cut,
	// for the same reason a truncated query result and a truncated schema
	// each carry one: a short answer presented as a complete one is a wrong
	// answer, and this is the participant's own text being shortened.
	//
	// Never set by a streamed export, which carries every statement whole.
	SQLTruncated bool
	Status       Status
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

// MaxHistorySQLChars bounds the statement one row of a *page* carries, in
// characters.
//
// The row count was bounded and the bytes were not, which is only half a
// bound (CLAUDE.md rule 2). sqlpolicy.MaxQueryBytes lets one statement be
// 64 KiB, MaxHistoryLimit lets one page hold two hundred of them, and
// nothing between the column and the browser said no: a single request for
// one's own log could be twelve megabytes, and a participant may ask for it
// as often as their rate budget allows.
//
// Applied in the SELECT rather than after the scan, so the bound holds where
// the bytes actually arrive (CLAUDE.md rule 12) — a driver reads a whole row
// before handing any of it over, so a cut made in Go has already paid for the
// allocation it exists to prevent. A thousand characters is at most four
// kilobytes of UTF-8, so a full page is under 800 KiB where it was 12.8 MiB,
// and it is far more of a statement than the panel's own one-line cell shows.
//
// It bounds the page, not the record: the CSV export streams every statement
// whole, one row at a time, and is where a participant goes for the text of
// something they wrote (see postgres.QueryLog.ExportHistory).
const MaxHistorySQLChars = 1000

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

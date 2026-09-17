package queryrunner

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// HistoryEntry is one row of a participant's own query log, read back rather
// than written — the same table Journal writes to (query_log), the other
// direction. It carries exactly what the log already records for this
// purpose: the statement, how it ended, and when.
type HistoryEntry struct {
	// ID is query_log.request_id, the row's only uuid column, reused as the
	// public identifier a client names in GET .../play/log/{entryId}
	// (§7). query_log's own primary key is a bigserial, added long before
	// any row needed a public identifier at all (migration 000004), and
	// giving it a surrogate uuid would be a migration this feature does not
	// otherwise call for.
	//
	// Reusing request_id relies on it actually being one row's own: every
	// row Begin opens carries the identifier of the HTTP request that ran
	// it (Entry.RequestID, from httpx.RequestID), and this installation's
	// own browser client never sends the header that lets a caller choose
	// it, so in practice it is always a fresh server-generated uuid and
	// distinct from every other row's. A participant who deliberately
	// replayed the same X-Request-Id on several of their own queries could
	// make their own history ambiguous by this identifier — Entry answers
	// with whichever of theirs matches first — but never another
	// participant's, since every lookup is also scoped to the caller's own
	// registration_id.
	ID  uuid.UUID
	SQL string
	// SQLTruncated says SQL is the beginning of the statement rather than the
	// whole of it — see MaxHistorySQLChars. A flag rather than a silent cut,
	// for the same reason a truncated query result and a truncated schema
	// each carry one: a short answer presented as a complete one is a wrong
	// answer, and this is the participant's own text being shortened.
	//
	// Never set by a streamed export, which carries every statement whole.
	// Entry sets it too, but only defensively: see its own doc.
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

// ErrHistoryEntryNotFound is Entry's refusal: no row of the caller's own
// registration has that request id. The same answer for a request id that
// was never logged at all and for one that belongs to another participant's
// row — telling those apart would confirm which request ids exist for
// somebody else's session.
var ErrHistoryEntryNotFound = errors.New("no such query log entry")

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

// MaxExportRows and MaxExportBytes bound one CSV download of a participant's
// own log — the read that is not paged, because a file is the whole record.
//
// "Not paged" was taken to mean "not bounded", and those are different things
// (CLAUDE.md rule 2). The export streams every row of a column that
// sqlpolicy.MaxQueryBytes lets reach 64 KiB, inside a transaction holding one
// of the core pool's ten connections for as long as the client chooses to
// read — the same core database every other participant's sign-in, submission
// and timer depends on. Unbounded, one participant's log is the size of one
// participant's patience.
//
// Both numbers are safety nets rather than budgets anybody spends. The
// installation's default rate is 30 queries a minute (QUERY_PER_MINUTE), so
// MaxExportRows is over eleven hours of asking without pause, against
// olympiads measured in hours; and MaxExportBytes is what makes that row count
// mean something, since a bound on rows with none on bytes is the same half a
// bound MaxHistorySQLChars was written for — at 64 KiB a statement, 20,000
// rows is 1.2 GiB. A real statement is a few hundred bytes, which puts a
// full-rate three-hour log around a megabyte and both bounds out of reach of
// anything a contest can produce.
//
// What a participant loses when one does bind: the file carries the oldest
// rows up to the bound and says so in a final line, rather than stopping
// silently. The newest rows are the ones the panel beside it shows.
const (
	MaxExportRows  = 20_000
	MaxExportBytes = 32 << 20
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

package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// One registration's whole query log as a file (§9.1), served from the play
// screen during the contest and from the profile after it. One implementation,
// so its bounds cannot drift.

// ExportGate is the set of keys with a CSV download open: a concurrency bound
// per caller, which a rate limit is not. An export holds a core-pool connection
// while the client reads, so a rate budget alone would let one account hold
// many at once. ExportSlots bounds the service as a whole; this one says "not
// the same file twice".
//
// The key is what the route passes: the registration for a participant's log
// (one per person per contest), the account for the organiser's contest feed.
// The play-screen and profile routes share one gate (internal/app,
// WithExports), so the bound does not depend on their admission rules never
// overlapping.
type ExportGate struct {
	mu      sync.Mutex
	holders map[uuid.UUID]struct{}
}

// NewExportGate returns a gate holding no downloads.
func NewExportGate() *ExportGate { return &ExportGate{} }

// enter claims the one slot for this key and returns its release; free is false
// when a download is already running.
func (e *ExportGate) enter(registration uuid.UUID) (release func(), free bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, busy := e.holders[registration]; busy {
		return func() {}, false
	}
	if e.holders == nil {
		e.holders = map[uuid.UUID]struct{}{}
	}
	e.holders[registration] = struct{}{}

	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.holders, registration)
	}, true
}

// queryLogCSVColumns is the header row and column order: what the panel shows,
// plus the error text.
var queryLogCSVColumns = []string{"executed_at", "status", "duration_ms", "row_count", "error", "sql"}

// queryLogCSVTruncatedNotice is the last line of a file a bound cut short. A
// data row, not a header, because the bound is known only after the status line
// went out, and in a spreadsheet the last row is visible. Its status cell is
// not a queryrunner status, so it never reads as a query.
var queryLogCSVTruncatedNotice = []string{"", "truncated", "", "", "This file carries the oldest " +
	"queries of your log up to the size one download may carry; the panel beside it shows the newest.", ""}

// exportDeadline is how long one CSV download may hold its database connection.
// The export streams inside a transaction, so a slow reader would otherwise pin
// a core-pool connection indefinitely; the row bounds do not help when the
// reader is the slow party.
//
// A full three-hour log is about a megabyte, seconds even on a poor network,
// and with ExportSlots capping concurrent holders a minute of them is a stall a
// contest recovers from. It is also the Retry-After a refused download gets
// (exportsBusy).
const exportDeadline = time.Minute

// queryLogCSVExport streams one registration's query log to a response.
type queryLogCSVExport struct {
	history QueryHistory
	exports *ExportGate
	slots   *ExportSlots
	// fail is the handler's own error mapping, so this file's refusals go
	// through the route's switch (CLAUDE.md rule 1).
	fail func(http.ResponseWriter, *http.Request, error)
	log  *slog.Logger
}

// serve writes the file for registration, named after contest. busy is called
// instead when that registration's download is already running, so each route
// refuses with its own code.
//
// The log is streamed, not paged (§9.1): each row goes to the socket as
// postgres.QueryLog.ExportHistory yields it, so memory stays flat. The bounds:
// MaxExportRows and MaxExportBytes for volume, with a final line when either
// binds; exportDeadline for time; ExportGate for one download per registration;
// ExportSlots for the service.
//
// The registration's gate is checked first; both checks are free, and "your own
// download is running" is the more useful refusal.
func (e queryLogCSVExport) serve(w http.ResponseWriter, r *http.Request, registration, contest uuid.UUID, busy func()) {
	release, free := e.exports.enter(registration)
	if !free {
		busy()
		return
	}
	defer release()

	// Before the transaction opens and takes a pool connection (CLAUDE.md rule
	// 13).
	releaseSlot, err := e.slots.enter()
	if err != nil {
		e.fail(w, r, err)
		return
	}
	defer releaseSlot()

	ctx, stop := context.WithTimeout(r.Context(), exportDeadline)
	defer stop()

	// Headers must be set before the first byte, and whether the read starts is
	// known only at the first row. open runs at most once, from the first row
	// or the empty case below, so an early failure can still answer with a
	// status.
	writer := csv.NewWriter(w)
	opened := false
	open := func() error {
		opened = true
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		// The identifier, not the title: a title can be any script and this
		// header is ASCII.
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", "query-log-"+contest.String()+".csv"))
		w.WriteHeader(http.StatusOK)
		return writer.Write(queryLogCSVColumns)
	}

	truncated, err := e.history.ExportHistory(ctx, registration, func(entry queryrunner.HistoryEntry) error {
		if !opened {
			if err := open(); err != nil {
				return err
			}
		}
		return writer.Write([]string{
			entry.ExecutedAt.UTC().Format(timeLayout),
			string(entry.Status),
			// Empty, not zero, for a query still running: an empty cell means
			// "not recorded".
			optionalNumber(entry.DurationMs),
			optionalNumber(entry.RowCount),
			// The same guard as the paged read, which matters more for a
			// document that gets kept and passed on. Both text cells are
			// defused for spreadsheets (spreadsheetSafe): `=1+1` is a valid
			// query and a formula to whoever opens the file.
			spreadsheetSafe(participantSafeError(entry.Status, entry.Error)),
			spreadsheetSafe(entry.SQL),
		})
	})
	if err != nil {
		if !opened {
			e.log.ErrorContext(r.Context(), "could not read the participant's query log for export", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return
		}
		// The 200 is already out; logged, because the log is the only place the
		// early stop is recorded.
		e.log.ErrorContext(r.Context(), "the participant's query log export stopped early", "error", err)
	}

	// A participant who ran nothing still gets a header row: a zero-byte
	// download looks like a failed one.
	if !opened {
		if err := open(); err != nil {
			e.log.ErrorContext(r.Context(), "could not write the query log export header", "error", err)
			return
		}
	}
	// A truncation must be visible in the file, or the participant would
	// believe it complete.
	if truncated {
		if err := writer.Write(queryLogCSVTruncatedNotice); err != nil {
			e.log.ErrorContext(r.Context(), "could not write the query log truncation notice", "error", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		e.log.ErrorContext(r.Context(), "could not finish the query log export", "error", err)
	}
}

// optionalNumber renders a count that may not have been recorded yet.
func optionalNumber(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

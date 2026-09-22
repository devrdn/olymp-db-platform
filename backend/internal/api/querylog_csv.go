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

// One registration's whole query log as a file (§9.1), served twice: from the
// play screen while the contest runs, and from the participant's own profile
// after it has ended for them. One implementation, because the bounds below
// are the interesting part and a second copy of them is a second place they
// could drift.

// ExportGate is the set of registrations with a CSV download open.
//
// It exists because a rate limit and a concurrency limit are different bounds,
// and only one of them was in place. AdmitRead lets an account start thirty
// reads a minute; the export is the one read that then *holds* a core-pool
// connection while the client reads it, so thirty starts a minute against a
// pool of 25 is a way to stop everybody else signing in — with a budget that
// never refuses anything, because nothing was asked for too often.
//
// It is a bound per caller, and the service needs one of its own on top of
// it: one download each is still every connection in the pool once there are
// 25 callers. That one is ExportSlots (export_slots.go); this one stays
// because "not the same file twice" is a different statement from "not more
// downloads than the pool can spare", and the second does not imply the first.
//
// The key is whatever the route hands it, and the two kinds of export claim
// different things. A participant's own log is claimed by the registration —
// one slot per person per contest, so an account on two contests may take both
// logs at once, and what is refused is the same log twice over. The
// organiser's contest feed is claimed by the account, because that file is a
// whole contest and one of them at a time is the point.
//
// The registration's two routes — the play screen's log and the same log on
// the profile afterwards — share one gate, handed to both handlers by
// internal/app (WithExports). Sharing it is what makes that bound structural:
// on their own the two routes never admit the same registration at the same
// moment, and a guarantee that rests on two admission rules agreeing is a
// guarantee that ends the day one of them changes.
//
// In this process only, which is all there is: the deployment is one API on
// one machine (docs/ARCHITECTURE.md §2.3), and a second replica would each get
// its own gate rather than none — a weaker bound, never a broken one. Nothing
// is persisted, so a restart forgets a download that a restart already ended.
type ExportGate struct {
	mu      sync.Mutex
	holders map[uuid.UUID]struct{}
}

// NewExportGate returns a gate holding no downloads.
func NewExportGate() *ExportGate { return &ExportGate{} }

// enter claims the one slot this registration has, and returns the release for
// it. free is false when a download of theirs is already running.
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

// queryLogCSVColumns is the file's header row, and the order of every row
// under it. The same facts the panel shows, plus the error text — a
// spreadsheet has room for it where a table column does not.
var queryLogCSVColumns = []string{"executed_at", "status", "duration_ms", "row_count", "error", "sql"}

// queryLogCSVTruncatedNotice is the last line of a file a bound cut short.
//
// A row in the data rather than a header, because the bound is only known once
// the rows have been counted and the status line went out long before that —
// and because a person opens this in a spreadsheet, where a header is
// invisible and the last row is not. The status cell is deliberately not one
// of queryrunner's own, so nothing reads it back as a query that happened.
var queryLogCSVTruncatedNotice = []string{"", "truncated", "", "", "This file carries the oldest " +
	"queries of your log up to the size one download may carry; the panel beside it shows the newest.", ""}

// exportDeadline is how long one CSV download may hold its database
// connection.
//
// The other half of the bound (queryrunner.MaxExportRows says how much may be
// read; this says for how long), and the half that is not about volume at all:
// the export streams inside a transaction, so a client reading a byte a second
// used to pin one of the core pool's connections for as long as it cared
// to — on the database every other participant's sign-in, submission and timer
// share. Nothing about the amount of data bounds that, because the slow party
// is the reader.
//
// A minute is generous against the file: a full-rate three-hour log is about a
// megabyte, which is under a second on the local network an olympiad runs on
// and a few seconds on a poor one. It is short against the damage: with
// ExportSlots capping how many are held at once, a minute of them is a stall
// a contest recovers from by itself. It is also what a refused download is
// told to wait (exportsBusy), because it is the longest a slot can be held.
const exportDeadline = time.Minute

// queryLogCSVExport streams one registration's query log to a response.
type queryLogCSVExport struct {
	history QueryHistory
	// exports keeps one registration to one download at a time; see
	// ExportGate.
	exports *ExportGate
	// slots keeps the whole service to as many downloads at once as the core
	// pool can spare; see ExportSlots.
	slots *ExportSlots
	// fail is the handler's own error mapping, so a refusal this file makes
	// is answered by the same switch every other refusal of that route goes
	// through (CLAUDE.md rule 1).
	fail func(http.ResponseWriter, *http.Request, error)
	log  *slog.Logger
}

// serve writes the file for registration, naming it after contest. busy is
// called instead when a download of this registration's is already running,
// and is the caller's own refusal — the two routes say it with their own
// codes.
//
// Streamed rather than paged — the opposite choice from the contest package
// next door, and for the opposite reason. A log has no natural page: a
// participant who never stops querying over a two-hour olympiad puts hundreds
// of rows in it, and a file quietly missing most of them is not a record of
// anything. So there is no page here (§9.1 is explicit that CSV streams row by
// row), and what keeps memory flat is that a row is written to the socket as
// it arrives: postgres.QueryLog.ExportHistory hands them over one at a time,
// csv.Writer's own bufio flushes when its buffer fills, and nothing
// accumulates a log's worth of anything.
//
// Streamed is not the same as unbounded. Four bounds, and each answers a
// different question. How much may be read is queryrunner.MaxExportRows and
// MaxExportBytes, both far past anything a contest can produce, with a final
// line in the file when either binds. How long the connection may be held is
// exportDeadline, because the amount of data does not bound a reader who is
// slow on purpose. How many of these one registration may have open at a
// time is one — see ExportGate: a bound per request is not a bound in
// aggregate when the rate budget allows a download to be started many times a
// minute and the pool has ten connections. And how many the whole service may
// have open at once is ExportSlots, because a bound per caller is not a bound
// on the pool either: every registration having one download is every
// connection being held.
//
// The registration's own gate is asked first. Both checks are free and both
// come before any read, so the order is only about which refusal is the more
// useful one to send: a second download of the same file is something the
// caller can fix by waiting for their own, and saying "the service is busy"
// to somebody whose own download is the thing in the way would be false.
func (e queryLogCSVExport) serve(w http.ResponseWriter, r *http.Request, registration, contest uuid.UUID, busy func()) {
	release, free := e.exports.enter(registration)
	if !free {
		busy()
		return
	}
	defer release()

	// Before the transaction below is opened, and before a connection is
	// taken from the pool this bound is about (CLAUDE.md rule 13).
	releaseSlot, err := e.slots.enter()
	if err != nil {
		e.fail(w, r, err)
		return
	}
	defer releaseSlot()

	// A ceiling on how long that connection may be held, whatever the client
	// does with the socket (exportDeadline).
	ctx, stop := context.WithTimeout(r.Context(), exportDeadline)
	defer stop()

	// Headers cannot be set once a byte is written, and whether the read even
	// starts is only known when the first row arrives (or the stream ends).
	// So the response is opened by this closure, called at most once: either
	// from the first row, or from the empty case below. A failure before it
	// runs still has a status line to spend, and spends it on saying so.
	writer := csv.NewWriter(w)
	opened := false
	open := func() error {
		opened = true
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		// The identifier, not the contest's title: a title is authored text
		// in any script and this header is ASCII.
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
			// Empty rather than zero for a row still running: an empty cell
			// is how CSV says "not recorded", and a zero here would read as a
			// query that took no time and returned nothing.
			optionalNumber(entry.DurationMs),
			optionalNumber(entry.RowCount),
			// The same guard the paged read applies, not a second opinion
			// about it: this file reads the same unsanitised column, and a
			// download is the more convenient way round a guard than a page
			// is, because it arrives as a document somebody keeps.
			participantSafeError(entry.Status, entry.Error),
			entry.SQL,
		})
	})
	if err != nil {
		if !opened {
			e.log.ErrorContext(r.Context(), "could not read the participant's query log for export", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return
		}
		// The status line is already out and said 200. Logged rather than
		// swallowed: what the participant has is a file that stops early, and
		// the log is the only place that fact survives.
		e.log.ErrorContext(r.Context(), "the participant's query log export stopped early", "error", err)
	}

	// A participant who ran nothing still gets a file: a header row and no
	// rows under it. A zero-byte download is indistinguishable from a failed
	// one.
	if !opened {
		if err := open(); err != nil {
			e.log.ErrorContext(r.Context(), "could not write the query log export header", "error", err)
			return
		}
	}
	// A file that stops at a bound says so in itself. A truncation nobody can
	// see in the file is the failure mode this whole line exists against: the
	// participant would have a record they believe is complete.
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

package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// The monitoring CSV exports (design §4): the whole feed of one participant,
// or of the whole contest, as a file, oldest first.
//
// Streamed like the participant's own query log (queryLogCSV), and read as a
// k-way merge of every source's own forward range (monitor.StreamFeed): each
// source a page at a time in its own index order, each row read once, one
// page per source in memory and no transaction held between pages.
//
// Bounded four ways: rows (maxMonitorExportRows) and bytes
// (maxMonitorExportBytes, counted as rows are written — CLAUDE.md rule 12),
// each ending the file with a line saying so; time (exportDeadline); and one
// download at a time per account (ExportGate). A file that stops for
// any other reason — the deadline, a failed read — ends with a line saying it
// is incomplete: the status line said 200 long before, and a file that
// quietly stops reads as a complete record.
//
// Every export is recorded (contest.monitor_export) before the first byte
// leaves; an export the trail cannot record is refused.

// maxMonitorExportRows and maxMonitorExportBytes bound one export. A
// contest-wide feed of a busy three-hour olympiad is tens of thousands of
// items, a few megabytes; a participant's is a few thousand.
const (
	maxMonitorExportRows  = 200_000
	maxMonitorExportBytes = 64 << 20
)

// monitorCSVColumns is the header row. data is the item's details as JSON,
// the same object the feed's response carries.
var monitorCSVColumns = []string{"at", "kind", "login", "full_name", "registration_id", "data"}

// monitorCSVTruncatedNotice is the last line of a file a bound cut.
var monitorCSVTruncatedNotice = []string{"", "truncated", "", "", "",
	"This file stops at the most one download may carry; narrow it to one participant or read the rest on the screen."}

// monitorCSVIncompleteNotice is the last line of a file that stopped
// because the download ran out of time or a read failed.
var monitorCSVIncompleteNotice = []string{"", "incomplete", "", "", "",
	"This file stopped early: the download ran out of time or a read failed. Download it again."}

// monitorCSVTooLargeNotice is the whole of a contest export's body when the
// contest has more participants than one export reads (monitor.ErrExportTooWide).
var monitorCSVTooLargeNotice = []string{"", "too_large", "", "", "",
	"This contest is too large to export whole; export its participants one at a time."}

// errExportBound stops the stream when a bound is reached.
var errExportBound = errors.New("the export reached its bound")

// contestCSV is GET /contests/{id}/monitor/export.csv.
func (h *MonitorHandler) contestCSV(w http.ResponseWriter, r *http.Request) {
	h.exportCSV(w, r, uuid.Nil, "monitor-"+monitorContest(r).String()+".csv")
}

// participantCSV is GET .../monitor/participants/{registrationID}/export.csv.
func (h *MonitorHandler) participantCSV(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	if _, err := h.watch.Participant(r.Context(), monitorContest(r), registration); err != nil {
		h.fail(w, r, err)
		return
	}
	h.exportCSV(w, r, registration, "monitor-"+registration.String()+".csv")
}

func (h *MonitorHandler) exportCSV(w http.ResponseWriter, r *http.Request, registration uuid.UUID, filename string) {
	identity, _ := auth.IdentityFrom(r.Context())
	release, free := h.exports.enter(identity.UserID)
	if !free {
		// A second download while the first still runs is asking faster
		// than the installation allows.
		httpx.Error(w, r, http.StatusTooManyRequests, codeMonitorTooOften,
			"A monitoring export of this account is still running; wait for it to finish")
		return
	}
	defer release()

	contest := monitorContest(r)
	if err := h.watch.RecordExport(r.Context(), identity.UserID, contest, registration); err != nil {
		h.fail(w, r, err)
		return
	}

	ctx, stop := context.WithTimeout(r.Context(), exportDeadline)
	defer stop()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	counter := &countingWriter{w: w}
	writer := csv.NewWriter(counter)
	if err := writer.Write(monitorCSVColumns); err != nil {
		h.log.ErrorContext(r.Context(), "could not write the monitoring export header", "error", err)
		return
	}

	rows := 0
	err := h.watch.StreamFeed(ctx, monitor.FeedQuery{Contest: contest, Registration: registration},
		func(item monitor.FeedItem) error {
			record := monitorCSVRow(item)
			if rows == h.exportRows || counter.n+recordSize(record) > int64(h.exportBytes) {
				return errExportBound
			}
			rows++
			if err := writer.Write(record); err != nil {
				return err
			}
			// Flushed as it goes, so the byte count is what really left.
			writer.Flush()
			return writer.Error()
		})
	switch {
	case err == nil:
	case errors.Is(err, errExportBound):
		err = writer.Write(monitorCSVTruncatedNotice)
	case errors.Is(err, monitor.ErrExportTooWide):
		err = writer.Write(monitorCSVTooLargeNotice)
	default:
		h.log.ErrorContext(r.Context(), "the monitoring export stopped early", "error", err)
		err = writer.Write(monitorCSVIncompleteNotice)
	}
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not write the monitoring export's last line", "error", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		h.log.ErrorContext(r.Context(), "could not finish the monitoring export", "error", err)
	}
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// recordSize is a record's size on the wire, near enough: its cells, a
// separator each, and room for quoting.
func recordSize(record []string) int64 {
	size := 0
	for _, cell := range record {
		size += len(cell) + 3
	}
	return int64(size)
}

// monitorCSVRow is one item as a row. Every cell a participant or an
// organiser typed is defused for spreadsheets (spreadsheetSafe).
func monitorCSVRow(item monitor.FeedItem) []string {
	data, err := json.Marshal(feedData(item.Data))
	if err != nil {
		data = []byte("{}")
	}
	return []string{
		monitorTime(item.At), item.Kind, spreadsheetSafe(item.Login), spreadsheetSafe(item.FullName),
		item.Registration.String(), spreadsheetSafe(string(data)),
	}
}

// spreadsheetSafe keeps a cell from being read as a formula: a spreadsheet
// evaluates a cell that starts with =, +, - or @, and a participant's full
// name or pasted text is theirs to choose. A leading apostrophe is how a
// spreadsheet is told "this is text".
func spreadsheetSafe(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}

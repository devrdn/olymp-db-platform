package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
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
// Streamed the way the participant's own query log is (queryLogCSV), and
// bounded the same three ways: rows (maxMonitorExportRows, with a last line
// saying so when it binds), time (exportDeadline), and one download at a
// time per account (inFlightExports). The file is read page by page through
// the feed's own keyset — each page a set of index ranges with a limit, no
// transaction held between them — so memory holds one page and no
// connection is pinned for the length of the download.
//
// Every export is recorded (contest.monitor_export) before the first byte
// leaves; an export the trail cannot record is refused.

// maxMonitorExportRows bounds one export. A contest-wide feed of a busy
// three-hour olympiad is tens of thousands of items; a participant's is a
// few thousand. Each row's statement is already cut to
// queryrunner.MaxHistorySQLChars by the feed, so rows bound bytes too.
const maxMonitorExportRows = 200_000

// monitorCSVColumns is the header row. data is the item's details as JSON,
// the same object the feed's response carries.
var monitorCSVColumns = []string{"at", "kind", "login", "full_name", "registration_id", "data"}

// monitorCSVTruncatedNotice is the last line of a file the row bound cut.
var monitorCSVTruncatedNotice = []string{"", "truncated", "", "", "",
	"This file stops at the most rows one download may carry; narrow it to one participant or read the rest on the screen."}

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
	writer := csv.NewWriter(w)
	if err := writer.Write(monitorCSVColumns); err != nil {
		h.log.ErrorContext(r.Context(), "could not write the monitoring export header", "error", err)
		return
	}

	// From before anything, forwards, a page at a time.
	cursor := monitor.Cursor{At: monitor.EarliestCursorTime, Source: monitor.SourceAudit, ID: "0"}
	written, truncated := 0, false
	for {
		page, err := h.watch.Feed(ctx, monitor.FeedQuery{Contest: contest, Registration: registration,
			After: &cursor, Limit: monitor.MaxFeedPage})
		if err != nil {
			// The status line already said 200; the log is where a file that
			// stops early is explained.
			h.log.ErrorContext(r.Context(), "the monitoring export stopped early", "error", err)
			break
		}
		for _, item := range page.Items {
			if written == maxMonitorExportRows {
				truncated = true
				break
			}
			if err := writer.Write(monitorCSVRow(item)); err != nil {
				h.log.ErrorContext(r.Context(), "the monitoring export stopped early", "error", err)
				return
			}
			written++
		}
		if truncated || !page.More || len(page.Items) == 0 {
			break
		}
		cursor = page.Items[len(page.Items)-1].Cursor()
	}
	if truncated {
		if err := writer.Write(monitorCSVTruncatedNotice); err != nil {
			h.log.ErrorContext(r.Context(), "could not write the monitoring export notice", "error", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		h.log.ErrorContext(r.Context(), "could not finish the monitoring export", "error", err)
	}
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

package api_test

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/monitor"
)

func (f *monitorFixture) count(action string) int {
	n := 0
	for _, a := range f.sink.Actions() {
		if a == action {
			n++
		}
	}
	return n
}

func TestAParticipantsFeedExportsAsCSVAndIsRecorded(t *testing.T) {
	f := newMonitorFixture(t)
	for range 2 {
		rec := f.get(f.one(f.reg)+"/export.csv", &f.organizer)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
			!strings.Contains(rec.Header().Get("Content-Disposition"), f.reg.String()) {
			t.Fatalf("export: %d %v", rec.Code, rec.Header())
		}
		rows, err := csv.NewReader(rec.Body).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0][1] != "kind" || rows[1][1] != "paste" || rows[1][4] != f.reg.String() ||
			!strings.Contains(rows[1][5], `"chars":3`) {
			t.Fatalf("file: %v", rows)
		}
		if f.store.lastFeed.Registration != f.reg || f.store.lastFeed.After == nil {
			t.Errorf("the export read %+v, want the participant's feed forwards", f.store.lastFeed)
		}
	}
	// Every export, not once per window.
	if got := f.count(audit.ActionContestMonitorExport); got != 2 {
		t.Errorf("two exports: %d entries, want 2", got)
	}
}

func TestTheContestsFeedExportsAsCSVAndIsRecorded(t *testing.T) {
	f := newMonitorFixture(t)
	rec := f.get(f.base()+"/export.csv", &f.organizer)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if f.store.lastFeed.Registration != uuid.Nil {
		t.Errorf("the contest export read one participant's feed: %+v", f.store.lastFeed)
	}
	if got := f.count(audit.ActionContestMonitorExport); got != 1 {
		t.Errorf("export entries: %d, want 1", got)
	}
	entry := f.sink.Entries[len(f.sink.Entries)-1]
	if entry.EntityID != f.contest.ID.String() || *entry.ActorID != f.organizer.ID {
		t.Errorf("entry = %+v", entry)
	}
}

func TestViewsAreRecordedOncePerOrganiserAndParticipant(t *testing.T) {
	f := newMonitorFixture(t)
	one := f.one(f.reg)
	for range 3 {
		for _, path := range []string{one + "/timeline", one + "/queries", one + "/workspace", one + "/answers"} {
			if rec := f.get(path, &f.organizer); rec.Code != http.StatusOK {
				t.Fatalf("%s: %d", path, rec.Code)
			}
		}
	}
	if got := f.count(audit.ActionContestMonitorView); got != 1 {
		t.Errorf("twelve reads of one participant: %d view entries, want 1", got)
	}
	for range 3 {
		f.get(f.base()+"/participants", &f.organizer)
		f.get(f.base()+"/feed", &f.organizer)
	}
	if got := f.count(audit.ActionContestMonitorView); got != 2 {
		t.Errorf("then the contest's table and feed: %d view entries, want 2", got)
	}
	// A refused read is not a view.
	f.get(f.one(f.otherReg)+"/timeline", &f.organizer)
	if got := f.count(audit.ActionContestMonitorView); got != 2 {
		t.Errorf("after a 404: %d view entries, want 2", got)
	}
}

func TestAnExportDefusesSpreadsheetFormulas(t *testing.T) {
	f := newMonitorFixture(t)
	rec := f.get(f.one(f.reg)+"/export.csv", &f.organizer)
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][3] != `'=HYPERLINK("http://x")` {
		t.Errorf("full name cell = %q, want it quoted as text", rows[1][3])
	}
	for _, row := range rows[1:] {
		for _, cell := range row {
			if cell != "" && strings.ContainsRune("=+@", rune(cell[0])) {
				t.Errorf("a cell starts a formula: %q", cell)
			}
		}
	}
}

// manyEvents is n events a second apart.
func manyEvents(n int, reg uuid.UUID) []monitor.FeedItem {
	items := make([]monitor.FeedItem, n)
	for i := range items {
		items[i] = monitor.FeedItem{Source: monitor.SourceEvent, ID: strconv.Itoa(i + 1),
			At: conteststest.FixtureNow.Add(time.Duration(i) * time.Second), Kind: string(monitor.KindPageLeft),
			Registration: reg, Login: "student", Data: json.RawMessage(`{"away_ms":2000}`)}
	}
	return items
}

func lastLine(t *testing.T, body string) []string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows[len(rows)-1]
}

func TestAnExportThatFailsMidwaySaysItIsIncomplete(t *testing.T) {
	f := newMonitorFixture(t)
	f.store.events = manyEvents(500, f.reg)
	f.store.failReads = 6 // the first page of every source, then a failure
	rec := f.get(f.base()+"/export.csv", &f.organizer)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if last := lastLine(t, rec.Body.String()); last[1] != "incomplete" {
		t.Errorf("last line = %v, want the incomplete notice", last)
	}
}

func TestAnExportStopsAtItsByteBoundAndSaysSo(t *testing.T) {
	f := newMonitorFixture(t)
	f.store.events = manyEvents(500, f.reg)
	f.handler.WithExportLimits(1_000_000, 4096)
	rec := f.get(f.base()+"/export.csv", &f.organizer)
	if rec.Body.Len() > 4096+200 {
		t.Errorf("the file is %d bytes, past its bound of 4096", rec.Body.Len())
	}
	if last := lastLine(t, rec.Body.String()); last[1] != "truncated" {
		t.Errorf("last line = %v, want the truncation notice", last)
	}
}

func TestAnExportStopsAtItsRowBoundAndSaysSo(t *testing.T) {
	f := newMonitorFixture(t)
	f.store.events = manyEvents(500, f.reg)
	f.handler.WithExportLimits(250, 1<<30)
	rec := f.get(f.base()+"/export.csv", &f.organizer)
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1+250+1 || rows[len(rows)-1][1] != "truncated" {
		t.Errorf("rows = %d, last = %v, want a header, 250 rows and the notice", len(rows), rows[len(rows)-1])
	}
}

func TestAWholeExportEndsWithItsLastRow(t *testing.T) {
	f := newMonitorFixture(t)
	f.store.events = manyEvents(500, f.reg)
	rec := f.get(f.base()+"/export.csv", &f.organizer)
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 501 || rows[500][1] != string(monitor.KindPageLeft) {
		t.Errorf("rows = %d, last = %v, want a header and every event", len(rows), rows[len(rows)-1])
	}
}

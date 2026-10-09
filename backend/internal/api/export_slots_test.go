package api_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// A CSV download holds a core-pool connection while the client reads it, so the
// bound is about the database. The per-registration gate cannot enforce it:
// each download here is a different registration. The bound is two, not the
// deployment's, so the test does not depend on the pool size.
func TestExportsPastTheServiceBoundAreRefused(t *testing.T) {
	const bound = 2
	f := newProfileFixture(t)
	f.slots = api.NewExportSlots(bound)
	f.router = f.mount()

	paths := []string{"/me/contests/" + f.finished.ID.String() + "/log.csv"}
	for range bound {
		paths = append(paths, "/me/contests/"+f.finishedContest(t).ID.String()+"/log.csv")
	}
	f.history.rows = []queryrunner.HistoryEntry{{SQL: "select 1", Status: queryrunner.StatusOK,
		ExecutedAt: conteststest.FixtureNow}}
	f.history.started, f.history.hold = make(chan struct{}, len(paths)), make(chan struct{})

	// Each download is held open at its first row, the state the bound exists
	// for.
	running := make(chan *httptest.ResponseRecorder, bound)
	for _, path := range paths[:bound] {
		go func() { running <- f.get(path, &f.student) }()
	}
	for range bound {
		<-f.history.started
	}

	// On a goroutine because an unbounded service would stream this one and
	// never return; the test should fail, not hang.
	over := make(chan *httptest.ResponseRecorder, 1)
	go func() { over <- f.get(paths[bound], &f.student) }()
	var refused *httptest.ResponseRecorder
	select {
	case refused = <-over:
	case <-time.After(10 * time.Second):
		close(f.history.hold)
		t.Fatalf("the download past the bound was served: it is streaming beside the other %d", bound)
	}
	if refused.Code != http.StatusServiceUnavailable || errorCode(t, refused) != "too_many_exports" {
		t.Fatalf("the download past the bound: %d %s, want 503 too_many_exports",
			refused.Code, refused.Body.String())
	}
	if seconds, err := strconv.Atoi(refused.Header().Get("Retry-After")); err != nil || seconds <= 0 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", refused.Header().Get("Retry-After"))
	}
	// Refused before reading anything: a bound that spends a connection to say
	// no is not a bound (CLAUDE.md rule 13).
	if calls := f.history.calls.Load(); calls != bound {
		t.Errorf("the query log was read %d times, want %d: the refused download reached the database", calls, bound)
	}

	// The ones under the bound stream their file.
	close(f.history.hold)
	for range bound {
		done := <-running
		if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), "select 1") {
			t.Fatalf("a download inside the bound: %d %s", done.Code, done.Body.String())
		}
	}

	// A finished download gives its slot back.
	f.history.started, f.history.hold = nil, nil
	if again := f.get(paths[bound], &f.student); again.Code != http.StatusOK {
		t.Errorf("the download after the others finished: %d %s", again.Code, again.Body.String())
	}
}

// The organiser's export draws on the same pool, so it shares the bound: two
// separate bounds could each fit the pool while their sum does not.
func TestTheOrganisersExportSharesTheServiceBound(t *testing.T) {
	slots := api.NewExportSlots(1)

	f := newProfileFixture(t)
	f.slots = slots
	f.router = f.mount()
	f.history.rows = []queryrunner.HistoryEntry{{SQL: "select 1", Status: queryrunner.StatusOK,
		ExecutedAt: conteststest.FixtureNow}}
	f.history.started, f.history.hold = make(chan struct{}, 1), make(chan struct{})

	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- f.get("/me/contests/"+f.finished.ID.String()+"/log.csv", &f.student) }()
	<-f.history.started

	m := newMonitorFixture(t)
	m.handler.WithExportSlots(slots)
	refused := m.get(m.base()+"/export.csv", &m.organizer)
	if refused.Code != http.StatusServiceUnavailable || errorCode(t, refused) != "too_many_exports" {
		t.Fatalf("the organiser's export while a participant's holds the only slot: %d %s, want 503 too_many_exports",
			refused.Code, refused.Body.String())
	}
	// Refused before the trail is written, which would also use the pool; an
	// export that never ran must not be recorded.
	if got := m.count(audit.ActionContestMonitorExport); got != 0 {
		t.Errorf("the refused export left %d entries in the trail, want 0", got)
	}

	close(f.history.hold)
	if rec := <-held; rec.Code != http.StatusOK {
		t.Fatalf("the participant's download: %d %s", rec.Code, rec.Body.String())
	}
	if rec := m.get(m.base()+"/export.csv", &m.organizer); rec.Code != http.StatusOK {
		t.Errorf("the organiser's export once the slot came back: %d %s", rec.Code, rec.Body.String())
	}
}

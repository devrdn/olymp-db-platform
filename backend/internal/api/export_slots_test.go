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

// A CSV download holds a core-pool connection for as long as the client takes
// to read it, so the number of them running at once is a number about the
// database and not about any one caller. The per-registration gate cannot say
// it: every download here is a different registration, so the gate lets all of
// them through and the pool is what runs out.
//
// The bound is set to two here rather than the deployment's own, so the test
// is about the rule and not about how many connections a compose file happens
// to give the pool.
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

	// As many downloads as the bound allows, each one held open at its first
	// row: this is the state the bound exists for, not a burst of starts.
	running := make(chan *httptest.ResponseRecorder, bound)
	for _, path := range paths[:bound] {
		go func() { running <- f.get(path, &f.student) }()
	}
	for range bound {
		<-f.history.started
	}

	// Asked on a goroutine and waited for, because an unbounded service
	// answers this one by streaming it: it would hold the log open like the
	// others and never return, and a test that simply called it would hang
	// instead of saying what went wrong.
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
	// And refused before it read anything: a bound on connections that spends
	// a connection to say no is not a bound (CLAUDE.md rule 13).
	if calls := f.history.calls.Load(); calls != bound {
		t.Errorf("the query log was read %d times, want %d: the refused download reached the database", calls, bound)
	}

	// The ones under the bound are untouched by it — they stream their file.
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

// The organiser's contest-wide export is the same shape and draws on the same
// pool, so it is counted against the same bound rather than against one of its
// own: two bounds of their own would each be under the pool's size and their
// sum over it, which is the arithmetic this whole bound exists to fix.
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
	// Refused before the trail was written, which is itself a write to the
	// pool this bound is protecting — and an export that never ran must not
	// be recorded as one that did.
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

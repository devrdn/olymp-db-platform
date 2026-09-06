package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// eventsFixture mounts the events endpoint behind a session, with a fake
// Access — the same fakeAccess participant_handler_test.go declares, reused
// here rather than a second implementation of "answer Access with whatever a
// test staged".
type eventsFixture struct {
	router   http.Handler
	handler  *api.EventsHandler
	access   *fakeAccess
	shutdown chan struct{}
	cookie   *http.Cookie
}

func newEventsFixture(t *testing.T) *eventsFixture {
	t.Helper()

	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "events-student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: userRepo,
		Authorizer: rbac.New(noRoles{}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	access := &fakeAccess{}
	shutdown := make(chan struct{})
	handler := api.NewEventsHandler(access, mw, log, shutdown)

	router := chi.NewRouter()
	handler.Mount(router)

	return &eventsFixture{
		router: router, handler: handler, access: access, shutdown: shutdown,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

// syncedBody serialises reads and writes so a test can inspect the
// recorder's buffer from the main goroutine while the handler under test is
// still writing to it from its own — httptest.ResponseRecorder's embedded
// bytes.Buffer is not otherwise safe for that.
type syncedRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newSyncedRecorder() *syncedRecorder {
	return &syncedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *syncedRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *syncedRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(status)
}

func (r *syncedRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *syncedRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func (r *syncedRecorder) Result() *http.Response {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Result()
}

// request builds a GET to the events endpoint, signed in, over a cancellable
// context so a test can end the connection the way a client disconnecting
// would.
func (f *eventsFixture) request(contestID uuid.UUID) (*http.Request, context.CancelFunc) {
	req := httptest.NewRequest(http.MethodGet, "/contests/"+contestID.String()+"/events", nil)
	req.AddCookie(f.cookie)
	ctx, cancel := context.WithCancel(req.Context())
	return req.WithContext(ctx), cancel
}

// serve runs the handler in its own goroutine against a synced recorder and
// returns a channel closed once ServeHTTP itself has returned — the signal
// every test below waits on before it is safe to inspect the response.
func (f *eventsFixture) serve(req *http.Request) (*syncedRecorder, <-chan struct{}) {
	rec := newSyncedRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.router.ServeHTTP(rec, req)
	}()
	return rec, done
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the connection did not end")
	}
}

const testResync = 15 * time.Millisecond

func TestEventsRequiresAuthentication(t *testing.T) {
	f := newEventsFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/contests/"+uuid.New().String()+"/events", nil)
	rec := httptest.NewRecorder()

	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body: %s)", rec.Code, rec.Body.String())
	}
}

// The same admission table participant_handler_test.go proves for the
// read endpoints, driven through this one: this is not a fourth
// implementation of "may this student be here", so a refusal from the same
// façade must become the same status and code here too.
func TestEventsMapsAccessRefusalsToTheDocumentedStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not a participant", queryproxy.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"contest not running", queryproxy.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{"participant finished", queryproxy.ErrFinished, http.StatusConflict, "contest_finished"},
		{"address not allowed", queryproxy.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEventsFixture(t)
			f.access.err = tc.err

			req, cancel := f.request(uuid.New())
			defer cancel()
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// Finding 3's own rule, proven here the way
// TestARateLimitRefusalIsA429AndNeverReachesAccess proves it for the read
// endpoints: a caller over budget is refused before Access ever runs.
func TestEventsRateLimitRefusalNeverReachesAccess(t *testing.T) {
	f := newEventsFixture(t)
	f.access.admitReadErr = queryrunner.ErrTooManyQueries

	req, cancel := f.request(uuid.New())
	defer cancel()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "query_too_often" {
		t.Fatalf("code = %q, want query_too_often", code)
	}
	if f.access.accessCalled {
		t.Fatal("Access was called after AdmitRead refused")
	}
}

// The one thing the check this task ends with asks first: does a connection
// carry this participant's own deadline and nothing else, computed by
// contests.Deadline and not by a second formula.
func TestEventsSendsServerNowAndTheParticipantsOwnDeadlineOnConnect(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	ends := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	f.access.participant = contests.Participant{ID: uuid.New()}
	fixedNow := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	f.handler.WithClock(func() time.Time { return fixedNow }).WithResyncInterval(time.Hour)

	req, cancel := f.request(contestID)
	rec, done := f.serve(req)

	waitForSubstring(t, rec, "event: sync")
	cancel()
	waitDone(t, done)

	body := rec.String()
	if !strings.Contains(body, "event: sync") {
		t.Fatalf("no sync event in the stream: %s", body)
	}
	if !strings.Contains(body, fixedNow.Format(time.RFC3339)) {
		t.Fatalf("sync event does not carry server_now = %s: %s", fixedNow.Format(time.RFC3339), body)
	}
	if !strings.Contains(body, ends.Format(time.RFC3339)) {
		t.Fatalf("sync event does not carry the participant's own deadline = %s: %s", ends.Format(time.RFC3339), body)
	}
	if !strings.Contains(body, "event: contest_started") {
		t.Fatalf("no contest_started event on connect: %s", body)
	}
	if !strings.Contains(body, `"status":"running"`) {
		t.Fatalf("contest_started does not carry the contest's status: %s", body)
	}
}

// An individual-timing participant who has not started yet has no deadline
// for contests.Deadline to compute (its own doc): the field must be absent
// rather than a synthetic value standing in for "not started".
func TestEventsOmitsTheDeadlineWhenThereIsNoneYet(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingIndividual}
	f.access.participant = contests.Participant{ID: uuid.New()} // StartedAt is nil
	f.handler.WithResyncInterval(time.Hour)

	req, cancel := f.request(contestID)
	rec, done := f.serve(req)
	waitForSubstring(t, rec, "event: sync")
	cancel()
	waitDone(t, done)

	if strings.Contains(rec.String(), `"deadline"`) {
		t.Fatalf("a deadline field was sent for a participant with no deadline yet: %s", rec.String())
	}
}

// Nothing about another participant may cross this channel: the response
// must never carry a second registration id, a second contest id or
// anything shaped like somebody else's business — only what this
// participant's own Access call resolved.
func TestEventsCarriesNothingButThisParticipantsOwnState(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	participantID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	f.access.participant = contests.Participant{ID: participantID}
	f.handler.WithResyncInterval(time.Hour)

	req, cancel := f.request(contestID)
	rec, done := f.serve(req)
	waitForSubstring(t, rec, "event: sync")
	cancel()
	waitDone(t, done)

	body := rec.String()
	if strings.Contains(body, participantID.String()) || strings.Contains(body, contestID.String()) {
		t.Fatalf("the stream names an identifier at all — it should carry only a status and two timestamps: %s", body)
	}
}

// The resync loop itself: more than one sync event arrives on one
// connection, spaced by the configured interval rather than only once on
// connect.
func TestEventsResyncsOnEveryTick(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.handler.WithResyncInterval(testResync)

	req, cancel := f.request(contestID)
	rec, done := f.serve(req)

	waitForCount(t, rec, "event: sync", 3)
	cancel()
	waitDone(t, done)
}

// The channel closes itself, with a contest_finished event, once Access
// starts reporting the contest is over for this participant — the scenario
// where the status catches up (or the participant's own deadline passes)
// while the connection is already open.
func TestEventsSendsContestFinishedAndClosesWhenAccessStartsRefusing(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.handler.WithResyncInterval(testResync)

	req, cancel := f.request(contestID)
	defer cancel()
	rec, done := f.serve(req)

	waitForSubstring(t, rec, "event: contest_started")
	f.access.setErr(queryproxy.ErrContestNotRunning)

	waitDone(t, done)
	body := rec.String()
	if !strings.Contains(body, "event: contest_finished") {
		t.Fatalf("no contest_finished event once the contest was no longer running: %s", body)
	}
	if !strings.Contains(body, `"status":"finished"`) {
		t.Fatalf("contest_finished does not carry the finished status: %s", body)
	}
}

// A refusal that is not about the contest's own clock (disqualification, an
// address that stopped being allowed) closes the channel without claiming
// the contest finished — that would be a different, wrong fact.
func TestEventsClosesWithoutContestFinishedForAnUnrelatedRefusal(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.handler.WithResyncInterval(testResync)

	req, cancel := f.request(contestID)
	defer cancel()
	rec, done := f.serve(req)

	waitForSubstring(t, rec, "event: contest_started")
	f.access.setErr(queryproxy.ErrNotAParticipant)

	waitDone(t, done)
	if strings.Contains(rec.String(), "contest_finished") {
		t.Fatalf("a disqualification was reported as the contest finishing: %s", rec.String())
	}
}

// Disconnect: cancelling the request context (the standard library's own
// signal that the client is gone) ends the handler and gives back its
// connection-limit slot, rather than leaving a goroutine running for a
// client that left.
func TestEventsReleasesItsSlotOnDisconnect(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	participant := contests.Participant{ID: uuid.New()}
	f.access.participant = participant
	f.handler.WithResyncInterval(time.Hour)

	req, cancel := f.request(contestID)
	rec, done := f.serve(req)
	waitForSubstring(t, rec, "event: sync")

	if got := f.handler.ActiveConnections(participant.ID); got != 1 {
		t.Fatalf("ActiveConnections() = %d, want 1 while the connection is open", got)
	}

	cancel()
	waitDone(t, done)

	if got := f.handler.ActiveConnections(participant.ID); got != 0 {
		t.Fatalf("ActiveConnections() = %d, want 0 after disconnect", got)
	}
}

// Shutdown: closing the channel passed to NewEventsHandler ends every open
// connection promptly, the same way a client disconnecting does, so a
// process shutdown does not wait out an SSE stream that would otherwise
// never end on its own.
func TestEventsEndOnShutdown(t *testing.T) {
	f := newEventsFixture(t)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.handler.WithResyncInterval(time.Hour)

	req, cancel := f.request(contestID)
	defer cancel()
	_, done := f.serve(req)

	select {
	case <-done:
		t.Fatal("the connection ended before shutdown was signalled")
	case <-time.After(50 * time.Millisecond):
	}

	close(f.shutdown)
	waitDone(t, done)
}

// The check this task ends with, first clause: a participant may not hold
// unbounded connections. Counted, not raced: two connections held open
// exhaust a limit of two, and a third is refused with 429 without ever
// touching the two already open.
func TestEventsEnforcesTheConnectionLimitByCounting(t *testing.T) {
	f := newEventsFixture(t)
	f.handler.WithMaxConnections(2).WithResyncInterval(time.Hour)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	participant := contests.Participant{ID: uuid.New()}
	f.access.participant = participant

	var cancels []context.CancelFunc
	var dones []<-chan struct{}
	for i := 0; i < 2; i++ {
		req, cancel := f.request(contestID)
		cancels = append(cancels, cancel)
		rec, done := f.serve(req)
		waitForSubstring(t, rec, "event: sync")
		dones = append(dones, done)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
		for _, done := range dones {
			waitDone(t, done)
		}
	}()

	waitForActiveConnections(t, f.handler, participant.ID, 2)

	req, cancel := f.request(contestID)
	defer cancel()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a third connection over the limit of 2 got status %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "too_many_connections" {
		t.Fatalf("code = %q, want too_many_connections", code)
	}
}

// Once a connection closes, its slot is free for another — the limit bounds
// how many are open at once, not how many an account may ever open.
func TestEventsFreesASlotForAnotherConnectionAfterOneCloses(t *testing.T) {
	f := newEventsFixture(t)
	f.handler.WithMaxConnections(1).WithResyncInterval(time.Hour)
	contestID := uuid.New()
	ends := time.Now().Add(time.Hour)
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &ends}
	participant := contests.Participant{ID: uuid.New()}
	f.access.participant = participant

	req1, cancel1 := f.request(contestID)
	rec1, done1 := f.serve(req1)
	waitForSubstring(t, rec1, "event: sync")

	req2, cancel2 := f.request(contestID)
	defer cancel2()
	rec2 := httptest.NewRecorder()
	f.router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second connection while the first still holds the only slot: status = %d, want 429", rec2.Code)
	}

	cancel1()
	waitDone(t, done1)
	waitForActiveConnections(t, f.handler, participant.ID, 0)

	req3, cancel3 := f.request(contestID)
	defer cancel3()
	rec3, done3 := f.serve(req3)
	waitForSubstring(t, rec3, "event: sync")
	cancel3()
	waitDone(t, done3)
}

// waitForSubstring polls rec's buffer until it contains want, or fails the
// test — a connection's handler goroutine writes asynchronously, so this is
// the synchronisation a test uses instead of a fixed sleep.
func waitForSubstring(t *testing.T, rec *syncedRecorder, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.String(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in the stream: %s", want, rec.String())
}

// waitForCount polls until rec's buffer contains want at least n times.
func waitForCount(t *testing.T, rec *syncedRecorder, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(rec.String(), want) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d occurrences of %q: %s", n, want, rec.String())
}

// waitForActiveConnections polls ActiveConnections instead of sleeping a
// fixed amount, so the test is not flaky under load and not slow when it
// does not need to be.
func waitForActiveConnections(t *testing.T, h *api.EventsHandler, id uuid.UUID, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.ActiveConnections(id) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ActiveConnections() never reached %d (last: %d)", want, h.ActiveConnections(id))
}

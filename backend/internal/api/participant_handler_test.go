package api_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// fakeAccess answers Access with whatever a test staged, so the handler's own
// mapping from a refusal to a status and a code can be exercised without a
// real queryproxy.Service behind it — that admission is proven where
// queryproxy owns it (internal/queryproxy).
type fakeAccess struct {
	participant contests.Participant
	contest     contests.Contest
	// err is guarded by mu rather than left a bare field: the events handler
	// tests (events_handler_test.go) change it while a connection's own
	// goroutine is concurrently calling Access on every resync tick — a
	// scenario nothing in this file needed until that one had a channel
	// left open across several ticks.
	mu  sync.Mutex
	err error
	// gotContestID records what Access was asked about, so a test can prove
	// the identifier came from the URL.
	gotContestID uuid.UUID
	// admitReadErr is what AdmitRead answers; nil means every caller is
	// admitted. accessCalled records whether Access was reached, so a test
	// can prove a refusal here stops the request before Access's own lookups.
	admitReadErr error
	accessCalled bool
	// admitReads counts AdmitRead calls, so a test can prove a request spent
	// its rate budget even though it was refused further in.
	admitReads int
	// delay, when set, is how long AccessForEvents waits before answering —
	// events_handler_test.go's own way of standing for a resync tick's two
	// lookups running slow (finding 2), without a real, adjustable-latency
	// store behind this fake.
	delay time.Duration
	// schema and schemaErr are what Schema answers, and schemaAsked records
	// which contest it was asked about — the same shape gotContestID gives
	// Access, for the same reason.
	schema      provisioning.Schema
	schemaErr   error
	schemaAsked uuid.UUID
}

func (a *fakeAccess) Schema(_ context.Context, contestID, _ uuid.UUID, _ netip.Addr) (provisioning.Schema, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.schemaAsked = contestID
	if a.schemaErr != nil {
		return provisioning.Schema{}, a.schemaErr
	}
	return a.schema, nil
}

func (a *fakeAccess) AdmitRead(uuid.UUID) error {
	a.mu.Lock()
	a.admitReads++
	a.mu.Unlock()
	return a.admitReadErr
}

func (a *fakeAccess) Access(_ context.Context, contestID, _ uuid.UUID, _ netip.Addr) (contests.Participant, contests.Contest, error) {
	a.accessCalled = true
	a.gotContestID = contestID
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.participant, a.contest, a.err
}

// AccessForEvents answers with whatever a test staged, exactly like Access —
// the events handler tests care about what the handler does with the
// participant and contest a test hands it, not about re-deriving
// queryproxy.Service's own admission rule (proven in its own package,
// internal/queryproxy/queryproxy_test.go). This fake never distinguishes the
// one status AccessForEvents admits that Access would not
// (contests.StatusPublished) — a test controls that simply by staging
// f.access.contest.Status itself.
func (a *fakeAccess) AccessForEvents(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error) {
	a.mu.Lock()
	delay := a.delay
	a.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return contests.Participant{}, contests.Contest{}, ctx.Err()
		}
	}
	return a.Access(ctx, contestID, userID, addr)
}

// setDelay stages how long the next AccessForEvents calls take to answer
// (finding 2), safely against a connection's own goroutine reading it
// concurrently on its next resync tick.
func (a *fakeAccess) setDelay(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.delay = d
}

// setErr changes what Access answers with, safely against a connection's own
// goroutine reading it concurrently on its next resync tick.
func (a *fakeAccess) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// setContest changes what Access answers a contest with, safely against a
// connection's own goroutine reading it concurrently on its next resync tick
// — a test's way of staging the published → running transition mid-connection
// (finding 4) without a second implementation of "who is this and are they
// still in".
func (a *fakeAccess) setContest(c contests.Contest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contest = c
}

// fakeHistory answers History with whatever a test staged, so the query log
// endpoint's own wiring and error mapping can be exercised without a real
// postgres.QueryLog behind it — that scoping is proven where it lives
// (internal/postgres/querylog_test.go).
type fakeHistory struct {
	items []queryrunner.HistoryEntry
	total int
	err   error
	// gotRegistration, gotLimit and gotOffset record what History was asked,
	// so a test can prove the registration came from Access rather than the
	// request, and that limit/offset came straight from the query string.
	gotRegistration uuid.UUID
	gotLimit        int
	gotOffset       int
	called          bool
	// exported is what ExportHistory streams, exportErr what it fails with,
	// and gotExportRegistration what it was asked about — the same three
	// facts the paged read above stages, for the CSV download beside it.
	exported              []queryrunner.HistoryEntry
	exportErr             error
	gotExportRegistration uuid.UUID
	exportCalled          bool
	// exportTruncated is what the read reports about a log longer than one
	// download may carry.
	exportTruncated bool
	// exportGate, when set, holds the read inside ExportHistory until it is
	// closed — the only way a test can have two downloads genuinely
	// overlapping rather than merely issued one after the other.
	exportGate chan struct{}
	// gotExportDeadline is the deadline the handler put on the read. Kept so
	// a test can assert the connection is held for a bounded time rather than
	// for as long as a client cares to read.
	gotExportDeadline time.Time
	// inside counts the reads currently held at exportGate, so a test can
	// wait for the first request to be demonstrably in the middle of one.
	// The mutex guards every field above that two goroutines touch.
	mu     sync.Mutex
	inside int
}

func (h *fakeHistory) ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) (bool, error) {
	h.mu.Lock()
	h.exportCalled = true
	h.gotExportRegistration = registrationID
	h.gotExportDeadline, _ = ctx.Deadline()
	gate, truncated, failure, rows := h.exportGate, h.exportTruncated, h.exportErr, h.exported
	h.inside++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.inside--
		h.mu.Unlock()
	}()

	if gate != nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-gate:
		}
	}
	for _, entry := range rows {
		if err := yield(entry); err != nil {
			return false, err
		}
	}
	return truncated, failure
}

// awaitInsideExport blocks until a read is actually inside ExportHistory.
//
// Without it the concurrency test is the kind that passes for the wrong
// reason: the second request would be refused, or admitted, depending on
// whether the first goroutine had been scheduled yet.
func (h *fakeHistory) awaitInsideExport(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		in := h.inside
		h.mu.Unlock()
		if in > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no export ever started")
}

func (h *fakeHistory) History(_ context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error) {
	h.called = true
	h.gotRegistration = registrationID
	h.gotLimit = limit
	h.gotOffset = offset
	return h.items, h.total, h.err
}

// fakeSubmitter answers Submit with whatever a test staged, so the answer
// endpoint's own request wiring and error mapping can be exercised without a
// real contests.Service behind it — that behaviour is proven where
// contests.Service owns it (internal/contests/submission_test.go).
type fakeSubmitter struct {
	outcome contests.SubmitOutcome
	err     error
	// gotCmd records what Submit was asked, so a test can prove the
	// question identifier and the value came from the request rather than
	// being invented here.
	gotCmd contests.SubmitCommand
	called bool
}

func (s *fakeSubmitter) Submit(_ context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error) {
	s.called = true
	s.gotCmd = cmd
	return s.outcome, s.err
}

// fixtureAnswersPerMinute is the answer rate every participant fixture is
// built with: low enough that a test can reach it in a few requests, high
// enough that a test posting one or two answers never meets it by accident.
const fixtureAnswersPerMinute = 3

// answerRate is the answer throttle a handler under test is built with: the
// real fixed-window limiter over the test's own cache, so what is counted is
// what the deployment counts.
func answerRate(c cache.Cache, perMinute int) api.AnswerRate {
	return api.AnswerRate{Limiter: auth.NewLimiter(c), PerMinute: perMinute}
}

// failingLimiter is an answer throttle whose counter cannot be kept.
type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, errors.New("cache unreachable")
}

// participantFixture mounts the participant endpoints behind a session, with
// a fake Access and a real Reader over in-memory stores — the same
// conteststest fakes internal/contests's own Reader tests use, so what is
// under test here is the handler's wiring and error mapping, not a second
// implementation of the reading rules.
type participantFixture struct {
	router    http.Handler
	access    *fakeAccess
	history   *fakeHistory
	submitter *fakeSubmitter
	stories   *conteststest.Stories
	questions *conteststest.Questions
	attempts  *conteststest.Attempts
	cookie    *http.Cookie
}

func newParticipantFixture(t *testing.T) *participantFixture {
	t.Helper()

	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})

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

	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	attempts := conteststest.NewAttempts()
	reader := contests.NewReader(stories, questions, attempts, nil)
	access := &fakeAccess{}
	history := &fakeHistory{}
	submitter := &fakeSubmitter{}

	router := chi.NewRouter()
	api.NewParticipantHandler(access, reader, history, submitter, answerRate(c, fixtureAnswersPerMinute), mw, log, "en").Mount(router)

	return &participantFixture{
		router: router, access: access, history: history, submitter: submitter,
		stories: stories, questions: questions, attempts: attempts,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

func (f *participantFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *participantFixture) post(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// TestAHiddenQuestionIsAbsentFromTheParticipantsList is the requirement
// section 6.1 is explicit about: a hidden question exists fully and is
// simply never shown.
func TestAHiddenQuestionIsAbsentFromTheParticipantsList(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}

	visible := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	hidden := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: false,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "candlestick"}},
	})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != visible.ID.String() {
		t.Fatalf("items = %+v, want exactly the visible question", payload.Items)
	}
	if strings.Contains(rec.Body.String(), hidden.ID.String()) {
		t.Fatalf("the hidden question's own identifier appeared in the response: %s", rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "weapon") {
		t.Fatalf("the hidden question's wording appeared in the response: %s", rec.Body.String())
	}
}

// Finding 1: the response must carry no display position at all. A dense
// ordinal — 1, 3, 4, 7 — would tell the caller exactly how many questions are
// hidden and precisely where each one sits, which is the one fact §6.1 says a
// participant must work out rather than read off a field. The items array
// already arrives in display order, so there is nothing an ordinal would add
// except that leak.
func TestTheQuestionsResponseCarriesNoOrdinal(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}

	// Three visible questions with two hidden ones between them, so a dense
	// staff ordinal would visibly skip (1, then 4, 5 — never 2 or 3).
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 1, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Q1"}},
	})
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 1, IsVisible: false,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Hidden A"}},
	})
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 3, Kind: contests.KindText, Points: 1, IsVisible: false,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Hidden B"}},
	})
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 4, Kind: contests.KindText, Points: 1, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Q2"}},
	})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"ord"`) {
		t.Fatalf("the response carries a display position, which counts and places the hidden questions: %s", rec.Body.String())
	}

	var payload struct {
		Items []struct {
			BodyMD string `json:"body_md"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 2 || payload.Items[0].BodyMD != "Q1" || payload.Items[1].BodyMD != "Q2" {
		t.Fatalf("items = %+v, want Q1 then Q2 in display order with nothing to say how far apart they are", payload.Items)
	}
}

// The reference answer must never appear anywhere in the response, for a
// visible question either.
func TestAReferenceAnswerNeverAppearsInTheQuestionsResponse(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}

	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"the butler", "match_kind", `"answers"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("the reference answer or its shape leaked into the response (%q): %s", leak, rec.Body.String())
		}
	}
}

// CLAUDE.md rule 1: every refusal queryproxy.Service.Access can answer with
// needs a declared sentinel, a mapping in fail(), and a test asserting the
// 4xx. This table is that test for every one of them, driven through both
// endpoints.
func TestAccessRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
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
			f := newParticipantFixture(t)
			f.access.err = tc.err
			contestID := uuid.New()

			for _, path := range []string{
				"/contests/" + contestID.String() + "/play/story",
				"/contests/" + contestID.String() + "/play/questions",
				"/contests/" + contestID.String() + "/play/log",
			} {
				rec := f.get(path)
				if rec.Code != tc.wantStatus {
					t.Fatalf("%s: status = %d, want %d (body: %s)", path, rec.Code, tc.wantStatus, rec.Body.String())
				}
				if code := errorCode(t, rec); code != tc.wantCode {
					t.Fatalf("%s: code = %q, want %q", path, code, tc.wantCode)
				}
			}
		})
	}
}

// A participant of another contest is refused the same way a stranger to
// every contest is — not_a_participant, never a 404 that would confirm the
// contest exists, and never a body that says anything about it.
func TestAParticipantOfAnotherContestLearnsNothingAboutThisOne(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.err = queryproxy.ErrNotAParticipant

	rec := f.get("/contests/" + uuid.New().String() + "/play/story")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "not_a_participant" {
		t.Fatalf("code = %q, want not_a_participant", code)
	}
}

// Finding 3: a caller over their own rate budget is refused before Access
// ever runs its lookups — the same order Run itself uses, and the same
// sentinel the console maps to 429 (queryrunner.ErrTooManyQueries,
// codeQueryTooOften).
func TestARateLimitRefusalIsA429AndNeverReachesAccess(t *testing.T) {
	for _, path := range []string{"story", "questions", "log"} {
		t.Run(path, func(t *testing.T) {
			f := newParticipantFixture(t)
			f.access.admitReadErr = queryrunner.ErrTooManyQueries

			rec := f.get("/contests/" + uuid.New().String() + "/play/" + path)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != "query_too_often" {
				t.Fatalf("code = %q, want query_too_often", code)
			}
			if f.access.accessCalled {
				t.Fatal("Access was called after AdmitRead refused — the rate check must run first, before Access's own lookups")
			}
		})
	}
}

// A caller within their own rate budget is unaffected: AdmitRead admits them
// and Access runs exactly as it always has.
func TestACallerWithinTheRateBudgetStillReachesAccess(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.contest = contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	if _, err := f.stories.Save(context.Background(), f.access.contest.ID, map[string]string{"en": "A body."}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	rec := f.get("/contests/" + f.access.contest.ID.String() + "/play/story")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !f.access.accessCalled {
		t.Fatal("Access was never called for a caller within their rate budget")
	}
}

// Finding 4: contests.ErrStoryNotFound must map to 404 — a sentinel with a
// mapping in fail() and, until now, no test asserting it.
func TestAMissingStoryIsA404(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	// No story saved: f.stories has nothing for contestID, so the reader
	// answers contests.ErrStoryNotFound.

	rec := f.get("/contests/" + contestID.String() + "/play/story")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "story_not_found" {
		t.Fatalf("code = %q, want story_not_found", code)
	}
}

// The story falls back per §6.2 when the requested language has no
// translation: the contest's own default, here English, answers instead of
// an error, and the response says which language it actually served.
func TestStoryFallsBackToTheContestsDefaultLanguage(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ru"}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	if _, err := f.stories.Save(context.Background(), contestID, map[string]string{
		"en": "A body in the stacks.",
		"ru": "Тело в архиве.",
	}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	// A language the contest does not offer at all (French) falls back to the
	// contest's own default (English), not to an error and not to whatever
	// the installation's own default happens to be.
	rec := f.get("/contests/" + contestID.String() + "/play/story?lang=fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Lang   string `json:"lang"`
		BodyMD string `json:"body_md"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lang != "en" || payload.BodyMD != "A body in the stacks." {
		t.Fatalf("payload = %+v, want the contest's own default language", payload)
	}

	// A language it does offer, Russian, is honoured.
	rec = f.get("/contests/" + contestID.String() + "/play/story?lang=ru")
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lang != "ru" || payload.BodyMD != "Тело в архиве." {
		t.Fatalf("payload = %+v, want the Russian story", payload)
	}
}

// The questions list also carries attempts remaining and closed, resolved
// from this participant's own attempts.
func TestQuestionsCarryAttemptsRemainingAndClosed(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registrationID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: registrationID}

	maxAttempts := 3
	q := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	f.attempts.Put(registrationID, q.ID, contests.AttemptStats{Attempts: 2})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ID                string `json:"id"`
			AttemptsRemaining *int   `json:"attempts_remaining"`
			Closed            bool   `json:"closed"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %+v, want 1", payload.Items)
	}
	got := payload.Items[0]
	if got.AttemptsRemaining == nil || *got.AttemptsRemaining != 1 {
		t.Fatalf("attempts_remaining = %v, want 1", got.AttemptsRemaining)
	}
	if got.Closed {
		t.Fatalf("closed = true, want false (one attempt remains)")
	}
}

// Finding 5: a reloaded question list must say whether a closed question was
// won, and for how much — the only way a participant can tell "closed
// because solved" from "closed because every attempt is spent" without
// re-submitting to find out.
func TestQuestionsCarryCorrectAndPointsAwarded(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registrationID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: registrationID}

	q := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	f.attempts.Put(registrationID, q.ID, contests.AttemptStats{Attempts: 1, Correct: true, PointsAwarded: 10})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			Correct       bool `json:"correct"`
			PointsAwarded int  `json:"points_awarded"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %+v, want 1", payload.Items)
	}
	if got := payload.Items[0]; !got.Correct || got.PointsAwarded != 10 {
		t.Fatalf("item = %+v, want {Correct: true, PointsAwarded: 10}", got)
	}
}

// A contest identifier that is not a UUID is a 400, not a 500 and not a call
// to Access with garbage.
func TestAnInvalidContestIDInTheURLIsA400(t *testing.T) {
	f := newParticipantFixture(t)

	rec := f.get("/contests/not-a-uuid/play/story")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.access.gotContestID != uuid.Nil {
		t.Fatalf("Access was called with %s for a URL that never named a contest", f.access.gotContestID)
	}
}

// TestParticipantRoutesDoNotShadowTheStaffContentEndpoints is a regression
// test for a defect this task's own review found rather than reasoned about:
// mounting this handler's story and questions routes at the same paths
// ContestsHandler already answers (/contests/{id}/story,
// /contests/{id}/questions) does not fail to build — chi's router silently
// lets the later Mount win — and with both mounted the way app.go actually
// mounts them, the staff-only endpoint stopped answering as itself at all.
// This assembles both handlers over one router exactly as app.go does and
// proves each still answers its own path: the organizer's endpoint still
// requires contest.view and returns the staff shape, and the participant's
// own view lives at its own /play prefix rather than contesting that URL.
func TestParticipantRoutesDoNotShadowTheStaffContentEndpoints(t *testing.T) {
	stores := conteststest.NewFixture()
	stores.Users.GrantRole("staff", "contest.create")
	owner := stores.Users.Add(users.User{Login: "organizer", FullName: "Organizer", Status: users.StatusActive, Roles: []string{"staff"}})
	contestID := stores.SeedPublishableContest().ID
	if err := stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: contestID, UserID: owner.ID, Role: rbac.RoleOwner, GrantedBy: owner.ID,
	}); err != nil {
		t.Fatalf("Grant() = %v", err)
	}

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: owner.ID, Login: owner.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: rbac.New(contestRoles{stores}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: token}

	// Both handlers mounted over one router, exactly as app.go mounts them.
	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(), stores.Sequence)
	access := &fakeAccess{err: queryproxy.ErrNotAParticipant}
	api.NewParticipantHandler(access, reader, &fakeHistory{}, stores.Service, answerRate(c, fixtureAnswersPerMinute), mw, log, "en").Mount(router)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// The organizer's own endpoint, unaffected: still answering with the
	// staff shape (a contest UUID and every translation) rather than the
	// participant's {lang, body_md}.
	staff := do("/contests/" + contestID.String() + "/story")
	if staff.Code != http.StatusOK {
		t.Fatalf("the staff story endpoint = %d, want 200 (body: %s)", staff.Code, staff.Body.String())
	}
	var staffBody struct {
		ID           string            `json:"id"`
		Translations map[string]string `json:"translations"`
	}
	if err := json.NewDecoder(staff.Body).Decode(&staffBody); err != nil {
		t.Fatalf("decode staff body: %v", err)
	}
	if staffBody.ID == "" || staffBody.Translations["en"] == "" {
		t.Fatalf("staff body = %+v, want the full staff projection", staffBody)
	}

	// The participant's own path lives at /play and is unaffected by the
	// staff route sharing a prefix — here refused, since the fake Access is
	// staged to refuse everyone, which is enough to prove the request
	// reached this handler's own admission check rather than the staff
	// route's RBAC gate (that would answer a bare 403 forbidden, not this
	// handler's own not_a_participant).
	participant := do("/contests/" + contestID.String() + "/play/story")
	if participant.Code != http.StatusForbidden {
		t.Fatalf("the participant story endpoint = %d, want 403 (body: %s)", participant.Code, participant.Body.String())
	}
	if code := errorCode(t, participant); code != "not_a_participant" {
		t.Fatalf("code = %q, want not_a_participant (proving this handler, not the staff route's RBAC, answered)", code)
	}
}

// The answer endpoint sends Submit exactly what the URL and the body carry —
// never a value invented by the handler, and never a participant or contest
// other than what Access just resolved.
func TestAnswerSubmitsTheURLsQuestionAndTheBodysValue(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	questionID := uuid.New()
	participantID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: participantID}

	rec := f.post("/contests/"+contestID.String()+"/questions/"+questionID.String()+"/answer",
		`{"value":"the butler"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !f.submitter.called {
		t.Fatal("Submit was never called")
	}
	got := f.submitter.gotCmd
	if got.QuestionID != questionID {
		t.Fatalf("QuestionID = %s, want %s (from the URL)", got.QuestionID, questionID)
	}
	if got.Value != "the butler" {
		t.Fatalf("Value = %q, want %q (from the body)", got.Value, "the butler")
	}
	if got.Participant.ID != participantID || got.Contest.ID != contestID {
		t.Fatalf("command = %+v, want the participant and contest Access resolved", got)
	}
}

// The response carries exactly what SubmitOutcome says — never more, never
// a reference answer.
func TestAnswerReturnsTheOutcome(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	remaining := 2
	f.submitter.outcome = contests.SubmitOutcome{
		Correct: true, PointsAwarded: 10, AttemptsRemaining: &remaining, Closed: true,
	}

	rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"yes"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Correct           bool `json:"correct"`
		PointsAwarded     int  `json:"points_awarded"`
		AttemptsRemaining *int `json:"attempts_remaining"`
		Closed            bool `json:"closed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Correct || payload.PointsAwarded != 10 || payload.AttemptsRemaining == nil ||
		*payload.AttemptsRemaining != 2 || !payload.Closed {
		t.Fatalf("payload = %+v, want the staged outcome", payload)
	}
}

// CLAUDE.md rule 1: every refusal contests.Service.Submit can answer with
// needs a declared sentinel, a mapping in fail(), and a test asserting the
// 4xx.
func TestAnswerRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"question not found", contests.ErrQuestionNotFound, http.StatusNotFound, "question_not_found"},
		{"answer too long", contests.ErrAnswerTooLong, http.StatusBadRequest, "answer_too_long"},
		// A choice question takes one of its option ids and nothing else; any
		// other string is the caller's own malformed request.
		{"answer not a choice", contests.ErrNotAChoice, http.StatusBadRequest, "answer_not_a_choice"},
		{"question closed", contests.ErrQuestionClosed, http.StatusConflict, "question_closed"},
		// §6.1.1: a sequential contest refuses an answer to a question a
		// registration has not opened yet, whatever the interface shows.
		{"question not open", contests.ErrQuestionNotOpen, http.StatusConflict, "question_not_open"},
		{"deadline passed", contests.ErrDeadlinePassed, http.StatusConflict, "deadline_passed"},
		// Finding 1: seven concurrent answers to the very same question can
		// run contests.Service.Submit out of retries; before this fix the
		// handler had no case for it and a real outage and this ordinary
		// contention answered the same way — internal_error, 500.
		{"too many concurrent attempts", contests.ErrTooManyAttemptConflicts, http.StatusConflict, "attempt_conflict"},
		{"not a participant", queryproxy.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"contest not running", queryproxy.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			contestID := uuid.New()
			f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
			f.access.participant = contests.Participant{ID: uuid.New()}
			f.submitter.err = tc.err

			rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"x"}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// A question identifier that is not a UUID is a 400, and Submit is never
// called with garbage.
func TestAnswerWithAnInvalidQuestionIDIsA400(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	rec := f.post("/contests/"+contestID.String()+"/questions/not-a-uuid/answer", `{"value":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.submitter.called {
		t.Fatal("Submit was called with an invalid question identifier")
	}
}

// A malformed body is a 400, and Submit is never called.
func TestAnswerWithAnInvalidBodyIsA400(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.submitter.called {
		t.Fatal("Submit was called with a body that could not be decoded")
	}
}

// Finding 1, end to end: the review's own scenario is seven simultaneous
// answers to the very same question outrunning contests.Service.Submit's own
// retry bound — every retry loses the attempt-number race, and Submit used
// to hand the caller an error the handler had no case for (a 500,
// indistinguishable from a real outage) rather than the refusal a
// participant can act on. This drives that scenario through the real
// contests.Service (not fakeSubmitter's canned error) and the real handler,
// so what is under test is Submit's own retry loop and fail()'s own mapping
// together, not either one asserted in isolation.
//
// conteststest.Submissions.ConflictsRemaining stands in for the seven
// simultaneous racers — the same technique
// TestSubmitGivesUpAfterTooManyConflicts uses at the service level (its own
// doc explains why: a Go map has no analogue of the table's own UNIQUE
// constraint racing two real transactions, so the genuine race is proven
// against PostgreSQL instead, in
// internal/postgres/submissions_test.go and
// TestInsertConcurrentlyNeverExceedsMaxAttemptsOrDuplicatesAnAttemptNumber).
// What this test adds on top is the part that repository-level proof cannot
// reach on its own: that running out of retries surfaces as a 4xx, not a
// 5xx.
func TestSevenConcurrentAnswersEndUpAsARefusalNotAnInternalError(t *testing.T) {
	stores := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	c := stores.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed,
		StartsAt: &starts, EndsAt: &ends,
	})
	user := stores.AddUser("racing-student")
	p := stores.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: user.ID, Status: contests.RegistrationActive,
	})
	q := stores.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	// However many of the seven actually lost every round, this is what it
	// looks like from Submit's side: its own retry loop never once sees
	// anything but a conflict.
	stores.Submissions.ConflictsRemaining = 1000

	c2 := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c2.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c2, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: user.ID, Login: user.Login})
	if err != nil {
		t.Fatalf("session Create() = %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: rbac.New(noRoles{}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	access := &fakeAccess{participant: p, contest: c}
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(), stores.Sequence)
	router := chi.NewRouter()
	// stores.Service, not a fakeSubmitter: what answers here is the real
	// retry loop.
	api.NewParticipantHandler(access, reader, &fakeHistory{}, stores.Service, answerRate(c2, fixtureAnswersPerMinute), mw, log, "en").Mount(router)

	req := httptest.NewRequest(http.MethodPost,
		"/contests/"+c.ID.String()+"/questions/"+q.ID.String()+"/answer",
		strings.NewReader(`{"value":"anything"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "attempt_conflict" {
		t.Fatalf("code = %q, want attempt_conflict (body: %s)", code, rec.Body.String())
	}
}

// Finding 3 (queryproxy's own, reused here): a caller over their own rate
// budget is refused before Access — and before Submit — ever run.
func TestAnswerRateLimitRefusalIsA429AndNeverReachesSubmit(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.admitReadErr = queryrunner.ErrTooManyQueries

	rec := f.post("/contests/"+uuid.New().String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"x"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "query_too_often" {
		t.Fatalf("code = %q, want query_too_often", code)
	}
	if f.access.accessCalled || f.submitter.called {
		t.Fatal("Access or Submit was reached after AdmitRead refused")
	}
}

// Answers have a budget of their own, per registration, far below the read
// budget a console query spends: an answer is a guess, and a guess repeated
// fast enough turns a candidate list into a solved question. The refusal says
// when to try again and never reaches grading.
func TestAnswersBeyondTheRegistrationsRateAreRefusedBeforeGrading(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	path := "/contests/" + contestID.String() + "/questions/" + uuid.New().String() + "/answer"

	for i := 0; i < fixtureAnswersPerMinute; i++ {
		if rec := f.post(path, `{"value":"x"}`); rec.Code != http.StatusOK {
			t.Fatalf("answer %d: status = %d, want 200 (body: %s)", i+1, rec.Code, rec.Body.String())
		}
	}

	f.submitter.called = false
	rec := f.post(path, `{"value":"x"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "answer_too_often" {
		t.Fatalf("code = %q, want answer_too_often", code)
	}
	if seconds, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || seconds <= 0 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", rec.Header().Get("Retry-After"))
	}
	if f.submitter.called {
		t.Fatal("Submit was reached by an answer over the rate")
	}
}

// CLAUDE.md rule 13: the budget counts attempts, not successes. A body that
// does not decode and an answer the service refuses both spent one, so a
// stream of malformed or refused guesses meets the limit as surely as a
// stream of graded ones.
func TestARefusedAnswerStillCountsAgainstTheAnswerRate(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	path := "/contests/" + contestID.String() + "/questions/" + uuid.New().String() + "/answer"

	if rec := f.post(path, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d, want 400", rec.Code)
	}
	if rec := f.post("/contests/"+contestID.String()+"/questions/not-a-uuid/answer", `{"value":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid question id: status = %d, want 400", rec.Code)
	}
	f.submitter.err = contests.ErrQuestionClosed
	if rec := f.post(path, `{"value":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("closed question: status = %d, want 409", rec.Code)
	}

	f.submitter.err = nil
	f.submitter.called = false
	rec := f.post(path, `{"value":"x"}`)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "answer_too_often" {
		t.Fatalf("status = %d, body %s; want 429 answer_too_often", rec.Code, rec.Body.String())
	}
	if f.submitter.called {
		t.Fatal("Submit was reached by an answer over the rate")
	}
}

// The budget belongs to the registration admission resolved, never to
// anything the request names: one participant spending theirs leaves another
// participant's untouched.
func TestTheAnswerRateIsKeptPerRegistration(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	path := "/contests/" + contestID.String() + "/questions/" + uuid.New().String() + "/answer"

	for i := 0; i <= fixtureAnswersPerMinute; i++ {
		f.post(path, `{"value":"x"}`)
	}

	f.access.participant = contests.Participant{ID: uuid.New()}
	if rec := f.post(path, `{"value":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("another registration: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

// A counter that cannot be kept is a protection that is not in place, and the
// answer is refused rather than graded unthrottled.
func TestAnAnswerIsRefusedWhenItsRateCannotBeCounted(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: userRepo, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	contestID := uuid.New()
	access := &fakeAccess{
		contest:     contests.Contest{ID: contestID, Status: contests.StatusRunning},
		participant: contests.Participant{ID: uuid.New()},
	}
	submitter := &fakeSubmitter{}
	router := chi.NewRouter()
	api.NewParticipantHandler(access, contests.NewReader(conteststest.NewStories(), conteststest.NewQuestions(), conteststest.NewAttempts(), nil),
		&fakeHistory{}, submitter, api.AnswerRate{Limiter: failingLimiter{}, PerMinute: 6}, mw, log, "en").Mount(router)

	req := httptest.NewRequest(http.MethodPost, "/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer",
		strings.NewReader(`{"value":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
	if submitter.called {
		t.Fatal("Submit was reached although the answer rate could not be counted")
	}
}

// CLAUDE.md rule 13: a value refused for not being one of a choice
// question's options still spent the caller's rate budget, which is checked
// before Submit ever reads the question; a stream of guesses is not free.
func TestAnAnswerRefusedAsNotAChoiceStillSpendsTheRateBudget(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.submitter.err = contests.ErrNotAChoice

	rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"abc"}`)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "answer_not_a_choice" {
		t.Fatalf("status = %d, body %s; want 400 answer_not_a_choice", rec.Code, rec.Body.String())
	}
	if f.access.admitReads != 1 || !f.submitter.called {
		t.Fatalf("AdmitRead called %d times, Submit called %v; want the budget spent once before Submit",
			f.access.admitReads, f.submitter.called)
	}
}

// The query log endpoint asks History for exactly the registration Access
// resolved — never a value the request itself carries — and hands the query
// string's limit and offset straight through, unmodified: clamping them is
// History's own job (queryrunner.NormalizeHistoryPage), not this handler's.
func TestQueryLogAsksHistoryForTheResolvedRegistrationAndThePagingParams(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registrationID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: registrationID}

	rec := f.get("/contests/" + contestID.String() + "/play/log?limit=10&offset=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !f.history.called {
		t.Fatal("History was never called")
	}
	if f.history.gotRegistration != registrationID {
		t.Fatalf("registration = %s, want %s (from Access, never the request)", f.history.gotRegistration, registrationID)
	}
	if f.history.gotLimit != 10 || f.history.gotOffset != 20 {
		t.Fatalf("limit=%d offset=%d, want 10 and 20 straight from the query string", f.history.gotLimit, f.history.gotOffset)
	}
}

// The response carries exactly what History returned, in its own vocabulary
// — never a reference to another registration, and a row still `running`
// carries no duration or row count rather than a false zero.
func TestQueryLogResponseCarriesTheHistoryEntries(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	duration, rows := 42, 7
	f.history.items = []queryrunner.HistoryEntry{
		{SQL: "SELECT * FROM suspects", Status: queryrunner.StatusOK, DurationMs: &duration, RowCount: &rows, ExecutedAt: time.Now()},
		{SQL: "SELECT pg_sleep(5)", Status: queryrunner.StatusRunning},
	}
	f.history.total = 2

	rec := f.get("/contests/" + contestID.String() + "/play/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			SQL        string `json:"sql"`
			Status     string `json:"status"`
			DurationMs *int   `json:"duration_ms"`
			RowCount   *int   `json:"row_count"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Total != 2 || len(payload.Items) != 2 {
		t.Fatalf("payload = %+v, want the two staged entries", payload)
	}
	if got := payload.Items[0]; got.SQL != "SELECT * FROM suspects" || got.Status != "ok" ||
		got.DurationMs == nil || *got.DurationMs != 42 || got.RowCount == nil || *got.RowCount != 7 {
		t.Fatalf("items[0] = %+v, want the completed entry's own outcome", got)
	}
	if got := payload.Items[1]; got.Status != "running" || got.DurationMs != nil || got.RowCount != nil {
		t.Fatalf("items[1] = %+v, want a running row with no duration or row count", got)
	}
}

// A page bounded in bytes has to say which rows it bounded. Shortening
// somebody's own query and presenting the result as what they wrote is the
// one thing a log must not do — so the row carries the flag, and a row that
// was not cut carries nothing (omitted, not `false`, the way every other
// "nothing to report" field on this response is).
func TestQueryLogSaysWhichRowsHadTheirStatementCut(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	f.history.items = []queryrunner.HistoryEntry{
		{SQL: "SELECT 1", Status: queryrunner.StatusOK, ExecutedAt: time.Now()},
		{SQL: "SELECT 'xxxx", Status: queryrunner.StatusOK, SQLTruncated: true, ExecutedAt: time.Now()},
	}
	f.history.total = 2

	rec := f.get("/contests/" + contestID.String() + "/play/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			SQL       string `json:"sql"`
			Truncated *bool  `json:"sql_truncated"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("payload carried %d rows, want 2", len(payload.Items))
	}
	if payload.Items[0].Truncated != nil {
		t.Errorf("a whole statement was reported as cut: %+v", payload.Items[0])
	}
	if payload.Items[1].Truncated == nil || !*payload.Items[1].Truncated {
		t.Errorf("a cut statement was handed over as if it were whole: %+v", payload.Items[1])
	}
}

// A failure to read the log is ours, not the participant's — the same
// treatment every other infrastructure failure on this handler gets
// (queryproxy.ErrUnavailable's own case in fail()).
func TestQueryLogReadFailureIsA500(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.err = errors.New("connection reset")

	rec := f.get("/contests/" + contestID.String() + "/play/log")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
}

// The CSV download of a participant's own query log (§9.1). What it must
// carry is exactly what the panel beside it already shows, in a shape a
// spreadsheet opens; what it must never carry is a row belonging to anybody
// else, which is decided by which registration it is asked about rather than
// by anything in the request.
func TestTheQueryLogCSVIsThisParticipantsOwnSessionAsAFile(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registration := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: registration}

	duration, rows := 12, 3
	f.history.exported = []queryrunner.HistoryEntry{
		{
			SQL: "SELECT * FROM guests", Status: queryrunner.StatusOK,
			DurationMs: &duration, RowCount: &rows,
			ExecutedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		},
		{
			// A statement carrying a comma and a quotation mark: CSV is only
			// a format if the quoting is real.
			SQL: `SELECT "name", 1 FROM guests`, Status: queryrunner.StatusRejected,
			Error:      "function_not_supported: pg_sleep",
			ExecutedAt: time.Date(2026, 3, 1, 10, 1, 0, 0, time.UTC),
		},
	}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", got)
	}
	if f.history.gotExportRegistration != registration {
		t.Fatalf("the log was read for registration %s, want the one Access resolved (%s)",
			f.history.gotExportRegistration, registration)
	}

	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v (%s)", err, rec.Body.String())
	}
	if len(records) != 3 {
		t.Fatalf("the file has %d lines, want a header and two rows: %v", len(records), records)
	}
	if got := strings.Join(records[0], ","); got != "executed_at,status,duration_ms,row_count,error,sql" {
		t.Fatalf("header = %q", got)
	}
	if records[1][0] != "2026-03-01T10:00:00Z" || records[1][1] != "ok" ||
		records[1][2] != "12" || records[1][3] != "3" || records[1][5] != "SELECT * FROM guests" {
		t.Errorf("first row = %v", records[1])
	}
	// A row still running has no duration and no row count, and an empty cell
	// is how CSV says "not recorded" — the same distinction the JSON page
	// keeps by omitting the field.
	if records[2][2] != "" || records[2][3] != "" {
		t.Errorf("a rejected row reported a duration or a row count: %v", records[2])
	}
	if records[2][4] != "function_not_supported: pg_sleep" || records[2][5] != `SELECT "name", 1 FROM guests` {
		t.Errorf("second row = %v", records[2])
	}
}

// An empty log is still a file: a header row and nothing else. A zero-byte
// download is indistinguishable from a failed one.
func TestTheQueryLogCSVOfAParticipantWhoRanNothingIsAHeaderRow(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	if len(records) != 1 || records[0][0] != "executed_at" {
		t.Fatalf("the file is %v, want exactly the header row", records)
	}
}

// The same admission every other participant endpoint goes through: somebody
// who is not in this contest is refused, and the log is never read on their
// behalf. Staff have no route to this one at all — it answers about the
// caller's own registration and about nothing else, so there is no
// participant to name in it.
func TestTheQueryLogCSVIsRefusedToSomebodyNotInTheContest(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.setErr(queryproxy.ErrNotAParticipant)

	rec := f.get("/contests/" + uuid.New().String() + "/play/log.csv")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := errorCode(t, rec); got != "not_a_participant" {
		t.Fatalf("code = %q", got)
	}
	if f.history.exportCalled {
		t.Fatal("the query log was read for a caller who is not in the contest")
	}
}

// CLAUDE.md rule 13, and the reason AdmitRead exists: a read that costs
// database round trips is charged the same budget a query is, before any of
// them are spent. Downloading the whole log is the most expensive read this
// handler offers, so it is the last one that should be free.
func TestTheQueryLogCSVSpendsTheSameRateBudgetAQueryDoes(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.admitReadErr = queryrunner.ErrTooManyQueries

	rec := f.get("/contests/" + uuid.New().String() + "/play/log.csv")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if f.access.accessCalled {
		t.Error("the contest was looked up for a caller the rate limit had already refused")
	}
	if f.history.exportCalled {
		t.Error("the query log was read for a caller the rate limit had already refused")
	}
}

// A failure before the first row still has a status line to spend, so it is
// spent on saying so rather than on a 200 carrying half a file.
func TestAQueryLogCSVThatFailsBeforeItStartsIsAnErrorNotAnEmptyFile(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exportErr = errors.New("the database is away")

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/csv") {
		t.Errorf("a failure was served as a CSV file: Content-Type = %q", got)
	}
}

// The CSV reads the same column the paged log does, so it goes through the
// same guard. `query_log.error_text` is written straight from the error a run
// produced, before anything above it sanitises anything — the file would
// otherwise be a second way round both of the console's own guards, and the
// more convenient one, because it arrives as a document somebody keeps.
func TestTheQueryLogCSVNeverHandsBackAFailureOfOurs(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exported = []queryrunner.HistoryEntry{
		{
			SQL:    "select * from guests;",
			Status: queryrunner.StatusError,
			Error: "connecting to the game database: failed to connect to `user=game_reader " +
				"database=game_pool_ce678159661a1_57c5ba38100c`: 127.0.0.1:5433 (localhost): " +
				`failed SASL auth: FATAL: password authentication failed for user "game_reader"`,
			ExecutedAt: time.Now().UTC(),
		},
		{
			// The validator refusing the participant's own text is a fact
			// about what they typed, and the most useful thing the file can
			// tell them. It stays.
			SQL:        "select pg_sleep(9);",
			Status:     queryrunner.StatusRejected,
			Error:      "function_not_supported: pg_sleep",
			ExecutedAt: time.Now().UTC(),
		},
	}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"game_reader", "game_pool_ce678159661a1", "5433", "password authentication"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("the file carries %q: %s", secret, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "function_not_supported: pg_sleep") {
		t.Fatalf("the validator's own words about the participant's query were dropped: %s", rec.Body.String())
	}
}

// The download used to be unbounded in three separate ways, and each one is
// its own test because each one is refused by a different mechanism.

// A file cut short by a bound has to say so inside itself. The alternative is
// a participant holding what they believe is the record of their session and
// is not — the same reason a truncated query result carries a flag.
func TestAQueryLogDownloadCutShortSaysSoInTheFileItself(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exported = []queryrunner.HistoryEntry{
		{SQL: "SELECT 1", Status: queryrunner.StatusOK, ExecutedAt: time.Now().UTC()},
	}
	f.history.exportTruncated = true

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("the file has %d lines, want a header, one row and the notice: %v", len(records), records)
	}
	last := records[len(records)-1]
	if last[1] != "truncated" || last[4] == "" {
		t.Fatalf("the last line is %v; a bound that bound has to be visible to whoever opens the file", last)
	}
	// And a complete file carries no such line, or every download would look
	// like a partial one.
	f.history.exportTruncated = false
	whole, err := csv.NewReader(f.get("/contests/" + contestID.String() + "/play/log.csv").Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	if len(whole) != 2 {
		t.Fatalf("a complete file has %d lines, want a header and one row: %v", len(whole), whole)
	}
}

// The read runs inside a transaction on the core pool, so the time it takes
// is a connection nobody else can have. Nothing about the size of the file
// bounds that — the slow party is the client — so the handler puts a deadline
// on it, and this is the assertion that the deadline is really there rather
// than the request context's own (which, for an HTTP server with no
// WriteTimeout, has none at all).
func TestAQueryLogDownloadHoldsItsConnectionForABoundedTime(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	before := time.Now()
	if rec := f.get("/contests/" + contestID.String() + "/play/log.csv"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.history.gotExportDeadline.IsZero() {
		t.Fatal("the read was given no deadline; a client reading a byte a second holds a pool connection for as long as it likes")
	}
	// The figure the handler chose is its own; what this pins is that it is a
	// figure at all and not one an operator would call unbounded. Two minutes
	// is the ceiling this test is willing to call bounded — ten of these held
	// that long is a stall a contest recovers from by itself.
	const tolerable = 2 * time.Minute
	if held := f.history.gotExportDeadline.Sub(before); held > tolerable {
		t.Fatalf("the read may hold its connection for %s, want at most %s", held, tolerable)
	}
}

// A bound per request is not a bound in aggregate. The rate budget allows
// thirty starts a minute and the core pool has ten connections, so an account
// that starts downloads and reads them slowly can hold every one of them
// while refusing nothing — which is sign-in, submission and the timer stopped
// for everybody else. One at a time per account is what closes that.
func TestASecondQueryLogDownloadWhileOneIsStillRunningIsRefused(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	// The first request is inside the read for as long as this takes, which
	// is what "still running" has to mean for the gate to be under test at
	// all. Released by the channel below rather than by a sleep.
	held := make(chan struct{})
	f.history.exportGate = held

	started := make(chan struct{})
	first := make(chan int, 1)
	go func() {
		close(started)
		first <- f.get("/contests/" + contestID.String() + "/play/log.csv").Code
	}()
	<-started
	// Wait until the first request is demonstrably inside ExportHistory, so
	// the second one cannot pass merely because the first had not started.
	f.history.awaitInsideExport(t)

	second := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("a second concurrent download answered %d, want 429: %s", second.Code, second.Body.String())
	}

	close(held)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the first download answered %d, want 200", code)
	}
	// And the slot is given back, or one download would be all an account
	// ever gets.
	f.history.exportGate = nil
	if again := f.get("/contests/" + contestID.String() + "/play/log.csv"); again.Code != http.StatusOK {
		t.Fatalf("a download after the first finished answered %d, want 200: %s", again.Code, again.Body.String())
	}
}
